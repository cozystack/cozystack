# proxmox-network-env

End-to-end suite for Proxmox-backed VPC subnets on a real Proxmox VE environment. It ships as `chainsaw-test.yaml.disabled`, so neither Chainsaw's discovery nor `hack/select-e2e.sh` counts it as a suite and no CI lane runs it. The CI-able part of the same feature is `hack/e2e-chainsaw/proxmox-network/`, which runs on the regular e2e cluster.

The suite drives `hack/e2e-chainsaw/_lib/run-proxmox-network.sh`, one phase per step: `preflight`, `vpc`, `control`, `datapath`, `capi`, `resilience`, `lifecycle` and `teardown`. `docs/proxmox-networking.md`, section 11, lists what each phase checks. Each check prints one TAP-style line, and a phase fails when any of its checks failed. `# SKIP` and `# TODO` lines never fail a phase. A TODO line marks an open question or a known limit from section 12 of that document.

It does not install Cozystack. It runs against an existing cluster and an existing hypervisor, and it changes both. It creates two VPCs, two Proxmox-backed Kubernetes clusters, and probe pods. It also restarts the proxmox-network controller and a kube-ovn-cni pod, and scales capmox to zero for a minute. Run it on an environment that is set aside for testing.

## Prerequisites

- A Cozystack cluster on Proxmox VE with the `cozystack.proxmox-network` package enabled, and its controller available.
- A `ProxmoxNetworkZone` with at least four free VLANs. The zone's provider network must be ready on every node it does not exclude. Each of those nodes needs a trunk NIC on the zone bridge, and the hypervisor side of `docs/proxmox-networking.md`, section 10, must be in place.
- An `egress` section on the zone, with its transit network and the transit router behind it.
- capmox and the in-cluster IPAM provider (`cozystack.capi-provider-infra-proxmox`, which brings `cozystack.capi-provider-ipam-in-cluster`).
- A Talos VM template that capmox can clone.
- Two tenant namespaces, for tenants A and B. Each must be a Tenant with `etcd: true`. Each must hold a Secret named `proxmox-credentials`, or the name given in `COZY_PXNET_CCM_SECRET`, with the Proxmox API credentials for the cloud-controller-manager. The keys are the ones `.github/workflows/e2e-proxmox.yaml` writes: `url`, `token_id`, `token_secret` and `region`.
- On the machine that runs the suite: `chainsaw` v0.2.15, `kubectl`, `jq`, and a kubeconfig for the cluster.

## Variables

| Variable | Required | Meaning |
|---|---|---|
| `COZY_PXNET_NS_A`, `COZY_PXNET_NS_B` | yes | The two tenant namespaces. |
| `COZY_PXNET_TEMPLATE_TAGS` | yes | Tags of the Talos VM template, comma-separated. capmox matches them as a set. |
| `COZY_PXNET_ZONE` | no | The zone. Empty takes the only one. |
| `COZY_PXNET_CCM_SECRET` | no | The credentials Secret in both namespaces. Defaults to `proxmox-credentials`. |
| `COZY_PXNET_PVE_POOL` | no | The Proxmox resource pool for the workers. Set it when the CCM token is scoped to a pool. |
| `COZY_PXNET_ALLOWED_NODES` | no | The Proxmox nodes capmox may use, comma-separated. Empty means all of them. |
| `COZY_PXNET_LB_RANGE` | no | `first-last` of the LoadBalancer addresses the transit router forwards to. Unset skips that check. |
| `COZY_PXNET_NS_NO_NETWORKS`, `COZY_PXNET_OTHER_BRIDGE` | no | A namespace that owns no Proxmox networks, and a bridge outside every zone. They are used for the admission checks about such a namespace. Unset skips those checks. |
| `COZY_PXNET_ETCD_NS` | no | The namespace of the control planes' datastore. Defaults to each tenant's own. |
| `COZY_PXNET_A_PRIVATE` … `COZY_PXNET_B_PODS` | no | The six subnet CIDRs. By default each is a /24 inside 10.208.0.0/12. |
| `COZY_PXNET_INTERNET_PROBE`, `COZY_PXNET_DNS_PROBE`, `COZY_PXNET_NTP_PROBE`, `COZY_PXNET_DNS_NAME` | no | Egress targets. The probes must be IPv4 addresses. |
| `COZY_PXNET_CLUSTER_TIMEOUT` | no | How many seconds a worker may take to join. Defaults to 1800. |
| `COZY_KEEP_TENANT` | no | Set to `true` to leave the clusters and VPCs in place after the run, for debugging. |

## Running

From the repository root:

```bash
export KUBECONFIG=/path/to/kubeconfig
export COZY_PXNET_NS_A=tenant-pxneta COZY_PXNET_NS_B=tenant-pxnetb
export COZY_PXNET_TEMPLATE_TAGS=capmox-template,cozystack,talos,talos-v1.13.6

chainsaw test \
  --config hack/e2e-chainsaw/.chainsaw.yaml \
  --test-dir hack/e2e-chainsaw/proxmox-network-env \
  --test-file chainsaw-test.yaml.disabled
```

`--test-file` with an extension loads that exact file, so the suite runs without being renamed. A full run takes roughly an hour, and most of that is spent on the two clusters' workers.

You can also run a single phase on its own, for example after fixing an environment problem:

```bash
hack/e2e-chainsaw/_lib/run-proxmox-network.sh preflight
```

Every phase reads its state from the cluster, so phases can run in separate invocations. Each phase needs the earlier ones to have run, though: `control` needs the VPCs from `vpc`, and `capi` reuses the probe pods from `datapath`.

## What stays out

Simulated VMs, meaning network namespaces on a hypervisor plugged into the zone bridge, need root on the Proxmox hosts. They are therefore not part of this suite. The Proxmox-backed Kubernetes clusters in the `capi` phase cover the same paths with real VMs.
