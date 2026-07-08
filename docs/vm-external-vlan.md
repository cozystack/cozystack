# Attaching a virtual machine to an external VLAN

This document describes how to attach a Cozystack virtual machine (the `vm-instance` application) directly to an external, physically-routed VLAN — the layer-2 segment a VM needs when it must appear on the same broadcast domain as external hardware (a licensing appliance, a storage box, a gateway managed outside the cluster), with an address from that VLAN's subnet rather than from the cluster overlay.

The default Cozystack VM networking is overlay-only (the pod network, plus optional KubeOVN VPC subnets). Bridging a VM onto a real VLAN is a different pattern and has one non-obvious constraint: **it works with a Linux bridge and the `bridge` CNI plugin, and it does not work with `macvlan`.** The rest of this guide explains why and gives a working recipe.

## Why `bridge` and not `macvlan`

KubeVirt attaches a VM interface to a secondary network using **bridge binding** by default (this is what the `vm-instance` chart emits for every network in `.spec.networks`). With bridge binding the guest's own MAC address is placed on the wire — the launcher pod does not masquerade or translate it.

A `macvlan` attachment is incompatible with that model. `macvlan` demultiplexes inbound frames strictly by the MAC address of the macvlan child interface. Because KubeVirt puts the *guest's* MAC on the wire — not the macvlan child's — replies from the gateway or other hosts arrive at the parent interface addressed to the guest MAC, do not match any macvlan child, and are silently dropped before they ever reach the VM. The symptom is a guest that can transmit (ARP requests and pings leave, visible in `tcpdump` on the parent interface) but never receives a reply (its neighbor entry for the gateway stays `FAILED`). As a secondary consequence, the host cannot reach macvlan children through the parent interface either, so a host-side service on the parent IP is unreachable from the VMs.

A **Linux bridge** does not have this limitation: it forwards by learned MAC on all bridged ports, so the guest MAC is reachable, and the host can carry an address on the bridge itself to talk to the VMs. Attach the VLAN sub-interface to a bridge and point a `bridge`-type NAD at it.

## Overview

Three pieces cooperate:

1. A **Linux bridge on each node** that enslaves the tagged VLAN sub-interface. This is node-level networking — it is configured by your node provisioning (netplan / Talos machine config / systemd-networkd), not by a Cozystack chart.
2. A **`NetworkAttachmentDefinition`** of type `bridge` referencing that bridge, created in the VM's tenant namespace.
3. The **`vm-instance`** application referencing the NAD by name in `.networks`, with the guest's static address supplied through cloud-init.

## Prerequisites

- The `multus` package is enabled (it provides the `NetworkAttachmentDefinition` CRD and the secondary-network plumbing).
- The `bridge` CNI plugin is present in `/opt/cni/bin` on every node. This binary comes from the node's CNI-plugins bundle, not from a Cozystack chart: on Talos it ships with the node image, while k3s keeps its copy under `/var/lib/rancher/k3s/data/current/bin` and leaves `/opt/cni/bin` (where Multus and Cilium install) without it. Verify with `ls /opt/cni/bin/bridge`; a missing binary makes the NAD fail with `failed to find plugin "bridge" in path [/opt/cni/bin]`. If it is absent, stage the upstream reference plugins (`bridge`, `macvlan`, `ipvlan`, …) into `/opt/cni/bin` as part of node provisioning.
- There is no IPAM plugin in this path — addresses are assigned inside the guest, not by the CNI. Plan static addresses per VM.

## Step 1 — Linux bridge on the node

Create a bridge that enslaves the tagged VLAN sub-interface. The VLAN sub-interface itself carries no address; the bridge carries the host's presence on that VLAN (optional, but useful for a gateway-reachability sanity path and for any host-side service the VMs must reach).

This example uses netplan on an Ubuntu/Debian node; the VLAN id and subnet are illustrative (`203.0.113.0/24`, VLAN 100, gateway `203.0.113.1`). Adapt to your uplink naming and to Talos/`systemd-networkd` if that is your provisioning:

```yaml
network:
  version: 2
  vlans:
    # Tagged VLAN sub-interface, no address of its own — enslaved to the bridge.
    uplink.100:
      id: 100
      link: uplink
  bridges:
    br100:
      interfaces:
        - uplink.100
      # Optional host presence on the VLAN. Keep the node's default route on
      # its management interface — do not add a default route here.
      addresses:
        - 203.0.113.2/24
```

Notes:

- The node's **default route must stay on the management interface.** The bridge address (if any) is only for on-VLAN reachability, not a second default gateway.
- `netplan apply` cannot move an interface into a bridge while a consumer still holds it (for example a `virt-launcher` pod using a previous `macvlan` attachment). Remove the consumer first (delete the VMI so the launcher releases the interface), then reconfigure.
- After a reboot, `systemd-networkd` may briefly report the bridge "routable" while the link is not yet actually up. If your VMs need the VLAN immediately at boot, gate their start on a reachability check, or re-run `netplan apply` until the gateway answers.

## Step 2 — NetworkAttachmentDefinition

Create a `bridge`-type NAD in the tenant namespace that will host the VM. The `vm-instance` chart resolves a network by name **in the VM's own namespace**, so one copy of the NAD must exist in every tenant namespace that runs VMs on this VLAN.

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: vlan100
  namespace: tenant-example
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "type": "bridge",
      "bridge": "br100",
      "ipam": {}
    }
```

- `bridge` must match the bridge name from step 1 (`br100` here).
- `ipam: {}` — no cluster-side address assignment; the guest configures its address itself (step 3).

## Step 3 — Attach the VM and assign a static address

Reference the NAD by name in the `vm-instance` values. Because the chart does not support `networkData`, the static address goes into cloud-init `userData` (`cloudInit`), written by the guest at first boot:

```yaml
# vm-instance values
instanceType: u1.medium
instanceProfile: ubuntu
disks:
  - name: example-system
networks:
  - name: vlan100
cloudInit: |
  #cloud-config
  write_files:
    - path: /etc/netplan/60-vlan100.yaml
      permissions: "0600"
      content: |
        network:
          version: 2
          ethernets:
            # Match the second NIC (the pod-network NIC is the first). Use the
            # interface that comes up without a DHCP lease.
            enp2s0:
              addresses:
                - 203.0.113.10/24
  runcmd:
    - netplan apply
```

The VM ends up with two interfaces: the always-present **pod-network** NIC (`default`, used for cluster-internal traffic and for the `vm-instance` external-access features) and the **VLAN** NIC. The `/24` address above brings up only the connected route for the VLAN subnet — it adds no default route, so the guest's egress stays wherever you want it (typically the pod NIC). If the VLAN is meant to be the guest's default gateway instead, add a default route under the VLAN NIC and remove it from the pod NIC.

## Gotchas

- **VMs are dual-homed.** The `vm-instance` chart always adds the pod-network NIC in addition to any `networks` you declare; there is no single-homed (VLAN-only) option today. Address the VLAN NIC inside the guest and leave the pod NIC to the cluster.
- **No `networkData`.** The chart wires cloud-init through `userData` only, so in-guest static configuration (netplan `write_files` + `netplan apply`, as above) is the way to assign the VLAN address.
- **MAC changes on VM re-creation.** KubeVirt generates a fresh guest MAC each time the VM object is re-created, and `vm-instance` exposes no way to pin it, so re-creating a VM changes its MAC. The upstream gateway then holds a stale ARP entry for the old MAC for a few minutes, so "gateway unreachable" immediately after re-creating a VM is expected — wait for the ARP entry to age out (~5 min) rather than treating it as a fault.
- **Host ↔ VM traffic.** If the host must talk to the VMs (a proxy, a health check), give the bridge a host address on the VLAN (step 1) — traffic through a bare VLAN sub-interface to bridge-attached guests will not work the way `macvlan` users expect.

## See also

- [`docs/gpu-vgpu.md`](./gpu-vgpu.md) — passing NVIDIA GPUs and vGPU profiles into the same VMs.
- KubeVirt user guide, [Interfaces and Networks](https://kubevirt.io/user-guide/network/interfaces_and_networks/) — bridge binding vs other binding methods.
