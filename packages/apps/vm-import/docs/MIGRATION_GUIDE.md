# VMware → Cozystack VM Migration Guide

This document describes how to migrate VMware vSphere virtual machines into
Cozystack using the `vm-import` application (built on
[Forklift / Konveyor](https://forklift.konveyor.io/)), the prerequisites it
needs, the risks involved, and the step-by-step procedure. It also records the
findings and known limitations discovered while validating the feature
end-to-end on a real cluster.

> Status: the import + conversion pipeline has been validated end-to-end (a
> real VMware UEFI AlmaLinux 9 VM was migrated and booted inside a tenant).
> Some productization items (see [Follow-up work](#follow-up-work)) are still
> open.

---

## 1. Overview

`vm-import` deploys two source/destination Forklift `Provider`s and a `Plan`
that drives the migration of one or more VMs. The disk transfer happens in one
of two modes:

| Mode | How the disk is read | Guest conversion | Needs |
|------|----------------------|------------------|-------|
| **virt-v2v** (default) | through the **vCenter** HTTP/NBD endpoint | yes — virtio drivers injected, VMware tools removed | the conversion pod can create a **user namespace** (passt) |
| **VDDK raw-copy** (`skipGuestConversion: true`) | **VDDK**, connecting **directly to the ESXi hosts** | no (disk copied as-is) | a **VDDK init image** + the guest must already have virtio |

Choosing the mode depends on your network topology and security posture (see
[Prerequisites](#3-prerequisites)).

After Forklift creates the KubeVirt `VirtualMachine`, the
`vm-adoption-controller` is meant to wrap it as a Cozystack `VMInstance` so it
becomes a managed, dashboard-visible workload in the user's tenant.

### Usage model

`vm-import` is a **tenant-scoped application**: a user deploys a `VMImport` in
their tenant (like any other Cozystack app), and the migrated VM ends up as a
managed `VMInstance` **in that same tenant**. The heavy lifting (the
privileged virt-v2v conversion) runs out-of-tenant in a platform-managed
privileged namespace and the result is adopted back into the tenant — this is
transparent to the user but requires the platform prerequisites in section 3.

### Inputs (what the user provides)

First, a **Secret** with vCenter credentials: keys `user`, `password`, and
either `thumbprint` or `insecureSkipVerify: "true"`.

Then the `VMImport` resource:

| Field | Required | Purpose |
|-------|----------|---------|
| `sourceUrl` | yes | vCenter SDK URL, e.g. `https://vcenter/sdk` |
| `sourceSecretName` | yes | name of the credentials Secret above |
| `vms[]` | to migrate | `{id: vm-123, name: target}` — the **MoRef** of each VM |
| `networkMap[]` | if the VM has NICs | `{sourceId: network-26, destinationType: pod\|multus, ...}` |
| `storageMap[]` | if the VM has disks | `{sourceId: datastore-13, storageClass: replicated}` |
| `warm` | optional | warm (incremental) vs cold migration |
| `enableAdoption` | optional (default true) | create the `VMInstance` in the tenant |
| `skipGuestConversion` | optional | raw-copy mode (no virt-v2v) — **requires `vddkInitImage`** |
| `vddkInitImage` | optional | VDDK init image (raw-copy / efficient transfer) |
| `virtV2vImage` | optional | override the conversion image |

The `vm-…`, `network-…`, `datastore-…` MoRef IDs come from the **Forklift
inventory** once the source `Provider` is connected. The dashboard should
present them as **dropdown lists** (VMs / networks / datastores) rather than
having the user type MoRef IDs by hand — that is the target self-service UX.

---

## 2. Architecture

```
vCenter (source VM)
   │  Forklift Provider (vsphere) + Plan + NetworkMap + StorageMap + Migration
   ▼
Conversion / disk copy  ── runs in a PRIVILEGED namespace ──┐
   │  (virt-v2v needs userns; a tenant's `baseline` PSA forbids it)
   ▼                                                         │
KubeVirt VirtualMachine + PVC  (in the conversion namespace) │
   │  vm-adoption-controller                                 │
   ▼                                                         ▼
Cozystack VMInstance (HelmRelease)  ── in the USER TENANT (tenant-*) ──
   → dashboard-managed VM with instanceType / instanceProfile / firmware
```

Key constraint: **the conversion pod cannot run in a user tenant** because
tenant namespaces enforce the `baseline` Pod Security Standard, which forbids
the `seccompProfile: Unconfined` (or `CAP_SYS_ADMIN`) that `virt-v2v`'s
`passt` networking needs to create a user namespace. Conversion must therefore
run in a dedicated **privileged** system namespace, and the resulting VM is
then adopted into the tenant.

---

## 3. Prerequisites

### 3.1 Cluster / platform
- Forklift operator and the `vm-import` app deployed (the `iaas` bundle).
- `cert-manager` (used by Forklift webhooks and, optionally, the seccomp webhook).

### 3.2 Network
- **vCenter must be reachable from the cluster** (the `sourceUrl` endpoint).
  This is required for **both** modes — virt-v2v reads the disk through vCenter.
- For **VDDK raw-copy** mode only: the cluster must **also reach the ESXi hosts
  directly** (VDDK uses NBD straight to the host that holds the VM). Verify
  there is no overlap between the **ESXi management network** and the cluster
  **Service CIDR** — an ESXi IP that falls inside the Service CIDR is shadowed
  by Kubernetes service routing and VDDK will fail with `server refused
  connection`.

### 3.3 Node configuration (virt-v2v mode)
- `virt-v2v`'s libguestfs appliance uses `passt`, which must create a **user
  namespace**. On Talos this is disabled by default; enable it on the nodes
  that run conversion pods:
  ```yaml
  machine:
    sysctls:
      user.max_user_namespaces: "11255"
  ```
  (No reboot required.) **Enabling unprivileged user namespaces is necessary
  but not sufficient** — see 3.4.

### 3.4 Conversion namespace & seccomp
- The conversion pod is created by Forklift with `seccompProfile:
  RuntimeDefault` and all capabilities dropped. Even with user namespaces
  enabled in the kernel, `RuntimeDefault` blocks the `clone(CLONE_NEWUSER)`
  syscall without `CAP_SYS_ADMIN`. Therefore the conversion pod needs
  `seccompProfile: Unconfined`.
- `Unconfined` is forbidden by the `baseline`/`restricted` PSA, so:
  - Run the import targeting a **dedicated namespace labelled
    `pod-security.kubernetes.io/enforce: privileged`** (not a user tenant).
  - Ensure the conversion pod (`forklift.app=virt-v2v`) actually gets
    `Unconfined`. Today Forklift hard-codes `RuntimeDefault`, so this requires
    either an upstream Forklift option or a small **mutating admission webhook**
    that sets `seccompProfile: Unconfined` on pods labelled
    `forklift.app=virt-v2v` in the conversion namespace.

### 3.5 VDDK init image (raw-copy mode)
The VMware VDDK is proprietary (Broadcom) and **not redistributable**. Build
the init image from the official SDK tarball and push it to a registry the
cluster can pull from:

```Dockerfile
FROM registry.access.redhat.com/ubi9/ubi-minimal
USER 1001
COPY vmware-vix-disklib-distrib /vmware-vix-disklib-distrib
RUN mkdir -p /opt
ENTRYPOINT ["cp", "-r", "/vmware-vix-disklib-distrib", "/opt"]
```
```sh
tar xzf VMware-vix-disklib-8.0.x-*.tar.gz   # -> vmware-vix-disklib-distrib/
docker build --platform=linux/amd64 -t <registry>/forklift-vddk:8.0.x .
docker push <registry>/forklift-vddk:8.0.x
```
Reference it via `VMImport.spec.vddkInitImage`. If the project is private,
attach an image pull secret to the service account used by the importer pods.

### 3.6 UEFI guests
If the source VM uses **UEFI firmware** (check the Forklift inventory:
`firmware: efi`), the resulting Cozystack VM must be configured for UEFI,
otherwise it hangs at the SeaBIOS `Booting from Hard Disk...` prompt. The
`firmware` field on the `vm-instance`/`VMInstance` API is added by
**[cozystack/cozystack#3002](https://github.com/cozystack/cozystack/pull/3002)**;
this migration feature depends on it for UEFI sources.

---

## 4. Risks

- **User namespaces** (3.3) widen the kernel attack surface and are an enabler
  for several container-escape CVEs — the reason Talos disables them. Prefer
  scoping the sysctl to a **dedicated migration node pool** (taint + the
  conversion pod's `convertorNodeSelector`) rather than the whole cluster.
- **`seccompProfile: Unconfined`** on conversion pods removes syscall
  filtering for those pods. Keep them confined to the dedicated privileged
  conversion namespace, never a user tenant.
- **The source VM is never modified.** virt-v2v reads the source disk
  read-only through an nbdkit **copy-on-write overlay**; conversion writes go
  to the copy, not back to vCenter. Forklift does not delete the source.
- **Cold migration powers the source VM off** during cutover (reversible). Run
  it on an already-powered-off VM when possible.
- **Static IPs / MAC are not preserved automatically.** A migrated guest whose
  NIC config is bound to the old MAC/interface, or that had a static IP on the
  VMware VLAN, will come up without networking on the pod network. Preserve the
  source MAC and/or reconfigure the guest for DHCP (or use a Multus network on
  the matching subnet).
- **Large disks take time.** During virt-v2v conversion the guest filesystem
  is read on demand from vCenter over HTTP (many small reads = slow); the bulk
  copy afterwards is sequential and faster.

---

## 5. Step-by-step

1. **Create the credentials Secret** in the conversion namespace with keys
   `user`, `password`, and either `thumbprint` or `insecureSkipVerify: "true"`.
2. **Create a `VMImport`** (minimal example):
   ```yaml
   apiVersion: apps.cozystack.io/v1alpha1
   kind: VMImport
   metadata:
     name: my-import
   spec:
     sourceUrl: https://vcenter.example.com/sdk
     sourceSecretName: vcenter-credentials
     # raw-copy mode (no userns, needs VDDK + ESXi reachability):
     # skipGuestConversion: true
     # vddkInitImage: registry.example.com/forklift-vddk:8.0.3
     vms:
       - id: vm-52          # MoRef id from the Forklift inventory
         name: my-vm
     networkMap:
       - sourceId: network-20
         destinationType: pod
     storageMap:
       - sourceId: datastore-14
         storageClass: replicated   # prefer an Immediate-binding class
   ```
   > Use an **`Immediate`**-binding StorageClass for the target disk.
   > `WaitForFirstConsumer` deadlocks with the CDI populator (the PVC waits for
   > a consumer that waits for the PVC).
3. **Discover VM / network / datastore IDs** from the Forklift inventory
   service once the source Provider is `Ready`
   (`/providers/vsphere/<uid>/vms`, `/networks`, `/datastores`).
4. **Watch the migration**: the `Plan`/`Migration` progress through
   `Initialize → DiskAllocation → ImageConversion → DiskTransferV2v →
   VirtualMachineCreation`.
5. **Adoption**: with `enableAdoption: true` (default) the
   `vm-adoption-controller` creates a `VMInstance` in the tenant. For UEFI
   sources, the `VMInstance` must set `firmware.bootloader: uefi` (see #3002).

---

## 6. Findings & fixes from end-to-end validation

Fixed in this PR:
- **forklift-operator**: inject `WATCH_NAMESPACE=cozy-forklift` (otherwise the
  Ansible role defaults `app_namespace` to `konveyor-forklift` and deploys the
  controller outside its RBAC scope, breaking install); substitute the
  unresolved `${VIRT_V2V_DONT_REQUEST_KVM}` placeholder.
- **vm-import**: the destination `openshift` Provider needs `secret: {}` or the
  admission webhook rejects it.
- **vm-import**: exposed `vddkInitImage`, `skipGuestConversion`, and
  `virtV2vImage` so both transfer modes and image overrides are configurable.

Known issues / upstream:
- **`virt-v2v-inspector: unrecognized option '--no-fstrim'`** — the
  release-2.11 controller invokes the inspector with a flag the bundled
  `forklift-virt-v2v:release-2.11` image doesn't support, failing the migration
  *after* a successful conversion+copy. Override with
  `virtV2vImage` or raise upstream. The floating `release-2.11` tags can drift
  out of sync between controller and conversion image.
- **Forklift hard-codes `svc.cluster.local`** for the inventory/controller
  endpoints; clusters with a different DNS domain need the cluster domain
  injected (or a hostAliases workaround).
- **Service-CIDR / ESXi overlap** breaks VDDK (see 3.2).

---

## 7. Follow-up work

The `vm-adoption-controller` must be extended to fully productize the flow:
- **Cross-namespace adoption**: conversion runs in the privileged system
  namespace, but the `VMInstance` (and its disk) must land in the user tenant.
  Clone/move the converted disk and create the `VMInstance` in the tenant.
- **Preserve the source MAC** on the created VM so the guest's NIC config keeps
  working.
- **Map the source firmware** (`bios`/`efi`) to `VMInstance.spec.firmware`
  (depends on #3002) — UEFI guests do not boot under SeaBIOS.
- **Map instanceType / instanceProfile** from the source VM's CPU/RAM/OS.
- Ship the conversion-namespace + seccomp mechanism as part of the platform.
