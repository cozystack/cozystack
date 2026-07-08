# Managed Kubernetes Service

## Managed Kubernetes in Cozystack

Whenever you want to deploy a custom containerized application in Cozystack, it's best to deploy it to a managed Kubernetes cluster.

Cozystack deploys and manages Kubernetes-as-a-service as standalone applications within each tenant’s isolated environment.
In Cozystack, such clusters are named tenant Kubernetes clusters, while the base Cozystack cluster is called a management or root cluster.
Tenant clusters are fully separated from the management cluster and are intended for deploying tenant-specific or customer-developed applications.

Within a tenant cluster, users can take advantage of LoadBalancer services and easily provision physical volumes as needed.                               
The control-plane operates within containers, while the worker nodes are deployed as virtual machines, all seamlessly managed by the application.

Kubernetes version in tenant clusters is independent of Kubernetes in the management cluster.
Users can select the supported patch versions from 1.31 to 1.35.

## Why Use a Managed Kubernetes Cluster?

Kubernetes has emerged as the industry standard, providing a unified and accessible API, primarily utilizing YAML for configuration.
This means that teams can easily understand and work with Kubernetes, streamlining infrastructure management.

Kubernetes leverages robust software design patterns, enabling continuous recovery in any scenario through the reconciliation method.
Additionally, it ensures seamless scaling across a multitude of servers,
addressing the challenges posed by complex and outdated APIs found in traditional virtualization platforms.
This managed service eliminates the need for developing custom solutions or modifying source code, saving valuable time and effort.

The Managed Kubernetes Service in Cozystack offers a streamlined solution for efficiently managing server workloads.

## Starting Work

Once the tenant Kubernetes cluster is ready, you can get a kubeconfig file to work with it.
It can be done via UI or a `kubectl` request:

-   Open the Cozystack dashboard, switch to your tenant, find and open the application page. Copy one of the config files from the **Secrets** section.
-   Run the following command (using the management cluster kubeconfig):

    ```bash
    kubectl get secret -n tenant-<name> kubernetes-<clusterName>-admin-kubeconfig -o go-template='{{ printf "%s\n" (index .data "admin.conf" | base64decode) }}' > admin.conf
    ```

There are several kubeconfig options available:

-   `admin.conf` — The standard kubeconfig for accessing your new cluster.
    You can create additional Kubernetes users using this configuration.
-   `admin.svc` — Same token as `admin.conf`, but with the API server address set to the internal service name.
    Use it for applications running inside the cluster that need API access.
-   `super-admin.conf` — Similar to `admin.conf`, but with extended administrative permissions.
    Intended for troubleshooting and cluster maintenance tasks.
-   `super-admin.svc` — Same as `super-admin.conf`, but pointing to the internal API server address.

## Implementation Details

A tenant Kubernetes cluster in Cozystack is essentially Kubernetes-in-Kubernetes.
Deploying it involves the following components:

-   **Kamaji Control Plane**: [Kamaji](https://kamaji.clastix.io/) is an open-source project that facilitates the deployment
    of Kubernetes control planes as pods within a root cluster.
    Each control plane pod includes essential components like `kube-apiserver`, `controller-manager`, and `scheduler`,
    allowing for efficient multi-tenancy and resource utilization.

-   **Etcd Cluster**: A dedicated etcd cluster is deployed using the cozystack [etcd-operator](https://github.com/cozystack/etcd-operator) (`etcd-operator.cozystack.io/v1alpha2`).
    It provides reliable and scalable key-value storage for the Kubernetes control plane.

-   **Worker Nodes**: Virtual Machines are provisioned to serve as worker nodes using KubeVirt.
    These nodes are configured to join the tenant Kubernetes cluster, enabling the deployment and management of workloads.
    Worker node disks automatically detect and match the underlying volume's block size
    (`blockSize.matchVolume`) to ensure compatibility with storage backends that use
    non-512-byte sectors, such as LINSTOR DRBD with 4Kn drives.

-   **Cluster API**: Cozystack is using the [Kubernetes Cluster API](https://cluster-api.sigs.k8s.io/) to provision the components of a cluster.

This architecture ensures isolated, scalable, and efficient tenant Kubernetes environments.

See the reference for components utilized in this service:

- [Kamaji Control Plane](https://kamaji.clastix.io)
- [Kamaji — Cluster API](https://kamaji.clastix.io/cluster-api/)
- [github.com/clastix/kamaji](https://github.com/clastix/kamaji)
- [KubeVirt](https://kubevirt.io/)
- [github.com/kubevirt/kubevirt](https://github.com/kubevirt/kubevirt)
- [github.com/cozystack/etcd-operator](https://github.com/cozystack/etcd-operator)
- [Kubernetes Cluster API](https://cluster-api.sigs.k8s.io/)
- [github.com/kubernetes-sigs/cluster-api-provider-kubevirt](https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt)
- [github.com/kubevirt/csi-driver](https://github.com/kubevirt/csi-driver)

## Breaking Changes

- **`ephemeralStorage` renamed to `diskSize`** (v1.4): The `nodeGroups[name].ephemeralStorage` field has been renamed to `nodeGroups[name].diskSize`. Existing clusters are migrated transparently by platform migration 41 during the pre-upgrade hook — no manual action is required. Newly written values should use `diskSize`. Existing VMs are rolling-updated via CAPI when the new values are applied. With the Talos worker rollover in this release the field now sizes the **single system disk** (Talos OS image streamed from `factory.talos.dev`, kubelet state, containerd image cache, local-path PVCs) — the pre-Talos `disk-kubelet` PVC layout was removed. State on that single disk persists across same-VM reboots (virt-launcher restart, guest reboot, node failure); VM replacement by CAPI (e.g. nodeGroup field change, MachineHealthCheck remediation) provisions a fresh disk.

- **Air-gapped tenant workers (Phase 1 Talos rollover)**: tenant worker bootstrap moves from Ubuntu containerDisk + kubeadm to a Talos OS disk image fetched over HTTP by CDI and a `.../installer/...` installer reference. Both sources are now overridable — set `talos.imageFactoryURL` (default `https://factory.talos.dev`) to a self-hosted Image Factory, a caching mirror, or an internal HTTP file server, and `talos.installerRepository` (default `factory.talos.dev/installer`) to a mirrored registry. The Helm-rendered `*-patch-containerd` Secret that previously plumbed the platform-wide `registries.mirrors` config to tenant workers (via the kubeadm template's containerd certs.d mount) has no consumer in the Talos machineconfig and was removed in this release; **in-guest container-image pulls** (pods pulling from registries inside the tenant cluster) still honour no per-tenant `registries.mirrors` override until Phase 2 restores it via `machine.registries.mirrors` knobs. So a strict-egress tenant can now mirror the Talos OS image and installer, but rate-limited in-guest registry pulls remain a Phase 2 follow-up — file an issue if you depend on this and it is not yet landed.

- **Worker MachineHealthCheck remediation is now enabled by default** (Phase 1 Talos rollover): the worker MHC `maxUnhealthy` moved from a hard-coded `0` (remediation effectively **off**) to a configurable `nodeHealthCheck.maxUnhealthy` defaulting to `"50%"`. CAPI now auto-remediates (deletes and replaces) unhealthy worker Machines while at least 50% of a group stays healthy. `"50%"` deliberately leaves headroom for transient unhealthy nodes during the kubeadm→Talos rollover and slow first boots from `factory.talos.dev`; set `nodeHealthCheck.maxUnhealthy: "0%"` to keep the previous remediation-off behaviour until your fleet is stable on Talos workers.

- **GitOps-managed tenant `Kubernetes` CRs must bump `spec.version` in Git** (Phase 1 Talos rollover): platform migration 46 patches live `kuberneteses.apps.cozystack.io` objects from `v1.30` to `v1.31` (v1.30 left the Talos↔Kubernetes support matrix). When the tenant `Kubernetes` CR is reconciled from Git (Flux/Argo), the next source reconcile re-applies `version: v1.30` over migration 46's patch, which then trips the chart's `_versions.tpl` guard and the HelmRelease fails. Update `spec.version` to `v1.31` (or newer) in your Git source before/with the platform upgrade. Tenants managed via the API or dashboard need no action — migration 46 handles them.

- **Remote-accessible LINSTOR StorageClasses are auto-propagated to tenants** (v1.5): infra-cluster LINSTOR StorageClasses whose `linstor.csi.linbit.com/allowRemoteVolumeAccess` is not `"false"` are created inside each tenant under the same name (provisioned by `csi.kubevirt.io`). **Upgrade caveat:** delete any *manually created* tenant StorageClass whose name collides with a propagated infra class (e.g. a hand-made `replicated`) before upgrading, or the propagated class cannot be created and the tenant CSI release will not converge. Infra classes that must stay node-local need `allowRemoteVolumeAccess: "false"` set explicitly — an absent annotation is treated as remote-accessible. Propagation is evaluated at HelmRelease render time, so classes added or removed on the infra cluster after a tenant exists propagate only on that tenant's next reconcile.

> The top-level `storageClass` field is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it. The per-node-group `nodeGroups[name].storageClass` field is intentionally **not** annotated immutable: it is optional and undefaulted, so a strict `self == oldSelf` rule would block any future attempt to set it on an existing node group.

## Parameters

### Common Parameters

| Name           | Description                                                                                                                              | Type     | Value        |
| -------------- | ---------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------------ |
| `storageClass` | Default StorageClass inside the tenant cluster. Remote-accessible LINSTOR classes are auto-propagated to the tenant under the same name. | `string` | `replicated` |


### Application-specific Parameters

| Name                                            | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | Type                | Value       |
| ----------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ----------- |
| `nodeGroups`                                    | Worker nodes configuration map. When left empty, a default node group `md0` is emitted with `minReplicas: 0` and `roles: [ingress-nginx]` — the MachineDeployment renders but provisions no workers until an unschedulable Pod triggers the cluster-autoscaler (or an operator scales the group manually). Enabling `addons.ingressNginx.enabled: true` on a CR with default `nodeGroups: {}` therefore requires either supplying a nodeGroup with `roles: [ingress-nginx]` and `minReplicas >= 1`, or waiting for the autoscaler to bring up the default md0 in response to the ingress-nginx controller Pods becoming Pending. Provide your own groups to take full control — they are not merged with the default, so you may name and omit groups freely. | `map[string]object` | `{}`        |
| `nodeGroups[name].minReplicas`                  | Minimum number of replicas.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `int`               | `0`         |
| `nodeGroups[name].maxReplicas`                  | Maximum number of replicas.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `int`               | `10`        |
| `nodeGroups[name].instanceType`                 | Virtual machine instance type.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `string`            | `u1.medium` |
| `nodeGroups[name].diskSize`                     | System disk size for the worker VM. Carries the Talos OS image (factory.talos.dev raw artifact streamed in by CDI), kubelet state, containerd image cache, and any local-path PVCs. Pre-Talos installs used a separate disk-kubelet PVC for kubelet/containerd state; on Talos this is consolidated onto the single system disk imaged from the factory artifact.                                                                                                                                                                                                                                                                                                                                                                                                 | `quantity`          | `20Gi`      |
| `nodeGroups[name].storageClass`                 | StorageClass for worker node persistent disks. When empty, falls back to the application-level storageClass. Worker VMs live-migrate, so their disks need ReadWriteMany — the RWX access mode is supplied by the chosen StorageClass's CDI StorageProfile, not set on the DataVolume here — and linstor-csi rejects RWX volumes that are not on a DRBD-backed StorageClass, so the fallback targets the replicated/DRBD application storageClass rather than a possibly non-DRBD cluster default. NOTE: deliberately not marked immutable — the field is optional and undefaulted, so a strict `self == oldSelf` rule would block any future attempt to set it on an existing node group.                                                                   | `string`            | `""`        |
| `nodeGroups[name].roles`                        | List of node roles.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | `[]string`          | `[]`        |
| `nodeGroups[name].resources`                    | Explicit CPU and memory for each worker node, as an alternative to `instanceType` sizing. Optional: when omitted, the node is sized by `instanceType`. When both `cpu` and `memory` are set, they take precedence and `instanceType` is ignored for that node group (the instancetype is omitted from the VM, since KubeVirt cannot override an instancetype's CPU/memory). Set both `cpu` and `memory` together or neither; setting only one is rejected at render time.                                                                                                                                                                                                                                                                                         | `object`            | `{}`        |
| `nodeGroups[name].resources.cpu`                | CPU available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `quantity`          | `""`        |
| `nodeGroups[name].resources.memory`             | Memory (RAM) available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `quantity`          | `""`        |
| `nodeGroups[name].gpus`                         | List of GPUs to attach (NVIDIA driver requires at least 4 GiB RAM).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | `[]object`          | `[]`        |
| `nodeGroups[name].gpus[i].name`                 | Name of GPU, such as "nvidia.com/AD102GL_L40S".                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `string`            | `""`        |
| `nodeGroups[name].kubelet`                      | Kubelet resource reservations for this node group.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | `object`            | `{}`        |
| `nodeGroups[name].kubelet.systemReservedMemory` | Memory reserved for host OS. Auto-computed from instanceType if empty.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `string`            | `""`        |
| `nodeGroups[name].kubelet.kubeReservedMemory`   | Memory reserved for kubelet and container runtime. Auto-computed from instanceType if empty.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | `string`            | `""`        |
| `nodeGroups[name].kubelet.systemReservedCpu`    | CPU reserved for host OS. Auto-computed from instanceType if empty.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | `string`            | `""`        |
| `nodeGroups[name].kubelet.kubeReservedCpu`      | CPU reserved for kubelet and container runtime. Auto-computed from instanceType if empty.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | `string`            | `""`        |
| `nodeGroups[name].kubelet.evictionHardMemory`   | Hard eviction threshold for memory (absolute like 200Mi or percentage like 7%).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `string`            | `7%`        |
| `nodeGroups[name].kubelet.evictionSoftMemory`   | Soft eviction threshold for memory (absolute like 1Gi or percentage like 10%).                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `string`            | `10%`       |
| `version`                                       | Kubernetes major.minor version to deploy                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | `string`            | `v1.35`     |
| `host`                                          | External hostname for Kubernetes cluster. Defaults to `<cluster-name>.<tenant-host>` if empty.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `string`            | `""`        |


### Cluster Addons

| Name                                          | Description                                                                                                                                                                    | Type       | Value     |
| --------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------- | --------- |
| `addons`                                      | Cluster addons configuration.                                                                                                                                                  | `object`   | `{}`      |
| `addons.certManager`                          | Cert-manager addon.                                                                                                                                                            | `object`   | `{}`      |
| `addons.certManager.enabled`                  | Enable cert-manager.                                                                                                                                                           | `bool`     | `false`   |
| `addons.certManager.valuesOverride`           | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.cilium`                               | Cilium CNI plugin.                                                                                                                                                             | `object`   | `{}`      |
| `addons.cilium.valuesOverride`                | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.gatewayAPI`                           | Gateway API addon.                                                                                                                                                             | `object`   | `{}`      |
| `addons.gatewayAPI.enabled`                   | Enable Gateway API.                                                                                                                                                            | `bool`     | `false`   |
| `addons.ingressNginx`                         | Ingress-NGINX controller.                                                                                                                                                      | `object`   | `{}`      |
| `addons.ingressNginx.enabled`                 | Enable the controller (requires nodes labeled `ingress-nginx`).                                                                                                                | `bool`     | `false`   |
| `addons.ingressNginx.exposeMethod`            | Method to expose the controller. Allowed values: `Proxied`, `LoadBalancer`.                                                                                                    | `string`   | `Proxied` |
| `addons.ingressNginx.hosts`                   | Domains routed to this tenant cluster when `exposeMethod` is `Proxied`.                                                                                                        | `[]string` | `[]`      |
| `addons.ingressNginx.valuesOverride`          | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.gpuOperator`                          | NVIDIA GPU Operator.                                                                                                                                                           | `object`   | `{}`      |
| `addons.gpuOperator.enabled`                  | Enable GPU Operator.                                                                                                                                                           | `bool`     | `false`   |
| `addons.gpuOperator.valuesOverride`           | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.hami`                                 | HAMi GPU virtualization middleware.                                                                                                                                            | `object`   | `{}`      |
| `addons.hami.enabled`                         | Enable HAMi (requires GPU Operator).                                                                                                                                           | `bool`     | `false`   |
| `addons.hami.valuesOverride`                  | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.fluxcd`                               | FluxCD GitOps operator.                                                                                                                                                        | `object`   | `{}`      |
| `addons.fluxcd.enabled`                       | Enable FluxCD.                                                                                                                                                                 | `bool`     | `false`   |
| `addons.fluxcd.valuesOverride`                | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.monitoringAgents`                     | Monitoring agents.                                                                                                                                                             | `object`   | `{}`      |
| `addons.monitoringAgents.enabled`             | Enable monitoring agents.                                                                                                                                                      | `bool`     | `false`   |
| `addons.monitoringAgents.valuesOverride`      | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.verticalPodAutoscaler`                | Vertical Pod Autoscaler.                                                                                                                                                       | `object`   | `{}`      |
| `addons.verticalPodAutoscaler.valuesOverride` | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.velero`                               | Velero backup/restore addon.                                                                                                                                                   | `object`   | `{}`      |
| `addons.velero.enabled`                       | Enable Velero.                                                                                                                                                                 | `bool`     | `false`   |
| `addons.velero.valuesOverride`                | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.coredns`                              | CoreDNS addon.                                                                                                                                                                 | `object`   | `{}`      |
| `addons.coredns.valuesOverride`               | Custom Helm values overrides.                                                                                                                                                  | `object`   | `{}`      |
| `addons.ouroboros`                            | Hairpin-NAT fix for ingress-nginx with PROXY-protocol.                                                                                                                         | `object`   | `{}`      |
| `addons.ouroboros.enabled`                    | Enable ouroboros. Requires addons.ingressNginx.enabled (chart-render fail otherwise). Only useful when PROXY-protocol is wired on the tenant ingress-nginx via valuesOverride. | `bool`     | `false`   |
| `addons.ouroboros.valuesOverride`             | Custom Helm values overrides. Operator-key wins over cozystack defaults.                                                                                                       | `object`   | `{}`      |


### Kubernetes Control Plane Configuration

| Name                                                | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | Type       | Value       |
| --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----------- |
| `controlPlane`                                      | Kubernetes control-plane configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `object`   | `{}`        |
| `controlPlane.replicas`                             | Number of control-plane replicas.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `int`      | `2`         |
| `controlPlane.apiServer`                            | API Server configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `object`   | `{}`        |
| `controlPlane.apiServer.resources`                  | CPU and memory resources for API Server.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | `object`   | `{}`        |
| `controlPlane.apiServer.resources.cpu`              | CPU available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `quantity` | `""`        |
| `controlPlane.apiServer.resources.memory`           | Memory (RAM) available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `quantity` | `""`        |
| `controlPlane.apiServer.resourcesPreset`            | Preset if `resources` omitted.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `string`   | `c1.medium` |
| `controlPlane.apiServer.extraArgs`                  | Extra command-line flags appended to the tenant kube-apiserver, passed through to KamajiControlPlane `spec.apiServer.extraArgs`. For OIDC use `spec.oidc.mode` — this passthrough is the escape hatch for other apiserver flags (`--requestheader-uid-headers=X-Remote-Uid`, feature gates, etc). Do NOT add legacy `--oidc-*` flags here when `spec.oidc.mode` is not `None`; the chart injects `--authentication-config` and the apiserver refuses to boot with both. Empty by default (no change to current behavior).                                                                                                                          | `[]string` | `[]`        |
| `controlPlane.apiServer.extraVolumes`               | Extra volumes added to the control-plane Deployment, passed through to KamajiControlPlane `spec.deployment.extraVolumes`. Use to mount a ConfigMap or Secret holding an AuthenticationConfiguration file referenced by `extraArgs`. Each item is a core/v1 Volume, but because the control-plane pod runs on the management cluster only `configMap` and `secret` sources are allowed (host-reaching sources like `hostPath`/`csi` and token-projection via `projected` are rejected); each volume must have a unique, non-empty name and exactly one source. The names `talos-ca` and `talos-tls-cert` are reserved by the chart. Empty by default. | `[]object` | `[]`        |
| `controlPlane.apiServer.extraVolumeMounts`          | Extra volume mounts added to the tenant kube-apiserver container, passed through to KamajiControlPlane `spec.apiServer.extraVolumeMounts`. Each `name` must reference a volume declared in `extraVolumes`; the chart-managed talos secret volumes cannot be mounted. Each item is a core/v1 VolumeMount. Empty by default.                                                                                                                                                                                                                                                                                                                           | `[]object` | `[]`        |
| `controlPlane.controllerManager`                    | Controller Manager configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                    | `object`   | `{}`        |
| `controlPlane.controllerManager.resources`          | CPU and memory resources for Controller Manager.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                     | `object`   | `{}`        |
| `controlPlane.controllerManager.resources.cpu`      | CPU available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `quantity` | `""`        |
| `controlPlane.controllerManager.resources.memory`   | Memory (RAM) available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `quantity` | `""`        |
| `controlPlane.controllerManager.resourcesPreset`    | Preset if `resources` omitted.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `string`   | `t1.micro`  |
| `controlPlane.scheduler`                            | Scheduler configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | `object`   | `{}`        |
| `controlPlane.scheduler.resources`                  | CPU and memory resources for Scheduler.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `object`   | `{}`        |
| `controlPlane.scheduler.resources.cpu`              | CPU available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `quantity` | `""`        |
| `controlPlane.scheduler.resources.memory`           | Memory (RAM) available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `quantity` | `""`        |
| `controlPlane.scheduler.resourcesPreset`            | Preset if `resources` omitted.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `string`   | `t1.micro`  |
| `controlPlane.konnectivity`                         | Konnectivity configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          | `object`   | `{}`        |
| `controlPlane.konnectivity.server`                  | Konnectivity Server configuration.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `object`   | `{}`        |
| `controlPlane.konnectivity.server.resources`        | CPU and memory resources for Konnectivity.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                           | `object`   | `{}`        |
| `controlPlane.konnectivity.server.resources.cpu`    | CPU available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `quantity` | `""`        |
| `controlPlane.konnectivity.server.resources.memory` | Memory (RAM) available.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                              | `quantity` | `""`        |
| `controlPlane.konnectivity.server.resourcesPreset`  | Preset if `resources` omitted.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `string`   | `t1.micro`  |
| `images`                                            | Optional image overrides for air-gapped or rate-limited registries.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | `object`   | `{}`        |
| `images.waitForKubeconfig`                          | Image used by the wait-for-kubeconfig init container. Empty falls back to images/busybox.tag.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                        | `string`   | `""`        |
| `images.kubectl`                                    | Image used by the bootstrap-token tenant Job (kubectl). Empty falls back to images/kubectl.tag.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                      | `string`   | `""`        |
| `images.talosCsrSigner`                             | Image used by the talos-csr-signer sidecar in the Kamaji control plane. Empty falls back to images/talos-csr-signer.tag.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | `string`   | `""`        |


### Talos Worker Image

| Name                        | Description                                                                                                                                                                                                                                                                                                                                               | Type     | Value                                                              |
| --------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------------------------------------------------------------------ |
| `talos`                     | Talos worker image configuration.                                                                                                                                                                                                                                                                                                                         | `object` | `{}`                                                               |
| `talos.version`             | Talos release used for worker OS image and installer. Must satisfy the chart's Talos<->Kubernetes support matrix against the chosen `version`.                                                                                                                                                                                                            | `string` | `v1.13.0`                                                          |
| `talos.schematicID`         | Talos image-factory schematic ID. Defaults to the cozystack-tested vanilla schematic. Operators using custom schematics (system extensions, kernel args) override here.                                                                                                                                                                                   | `string` | `ce4c980550dd2ab1b17bbf2b08801c7eb59418eafe8f279833297925d67c7515` |
| `talos.imageFactoryURL`     | Base URL of the Talos Image Factory that serves the worker OS disk image (the `openstack-amd64.raw.xz` raw artifact streamed in by CDI over HTTP). Defaults to the public factory. Point at a self-hosted Image Factory, a caching mirror, or an internal HTTP file server for air-gapped, rate-limited, or flaky-egress environments. No trailing slash. | `string` | `https://factory.talos.dev`                                        |
| `talos.installerRepository` | OCI repository prefix for the Talos installer image used by the in-guest `talos-reconcile` upgrade Job. Resolved as `<installerRepository>/<schematicID>:<version>`. Defaults to the public factory's installer path. Override for air-gapped or mirrored registries. No trailing slash.                                                                  | `string` | `factory.talos.dev/installer`                                      |


### Node Health Check

| Name                                 | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | Type     | Value |
| ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------- | ----- |
| `nodeHealthCheck`                    | MachineHealthCheck tuning for worker node groups.                                                                                                                                                                                                                                                                                                                                                                                                                                                      | `object` | `{}`  |
| `nodeHealthCheck.maxUnhealthy`       | Maximum number of unhealthy nodes tolerated per node group before remediation is paused. The MHC admission webhook accepts either a bare integer ("0", "1", ...) or a percentage ("0%", "50%"); bare numeric strings are rejected, so the safer default is to express the value as a percentage. Default "50%" leaves headroom for transient unhealthy nodes during the kubeadm-to-Talos rollover and slow first boots from factory.talos.dev. Drop to "0%" once the fleet is stable on Talos workers. | `string` | `50%` |
| `nodeHealthCheck.nodeStartupTimeout` | Maximum time a Machine is allowed to spend reaching the Ready condition before it is remediated. Raise for slow first boots (Talos image fetch from factory.talos.dev or a busy storage class on the kubevirt-csi PVC populator).                                                                                                                                                                                                                                                                      | `string` | `10m` |


### OIDC

| Name                               | Description                                                                                                                                                                                                                                                                                                                                       | Type       | Value  |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------ |
| `oidc`                             | OIDC authentication and per-user RBAC for the tenant kube-apiserver. See docs/oidc-tenant.md for the operator guide.                                                                                                                                                                                                                              | `object`   | `{}`   |
| `oidc.mode`                        | Identity mode. `None`: no OIDC, only the static admin kubeconfig works. `System`: trust the platform `cozy` realm via a per-cluster public client with audience binding; zero-config default. `CustomConfig`: trust a tenant-supplied issuer directly (BYO); `cozy` is not in the path.                                                           | `string`   | `None` |
| `oidc.customConfig`                | Tenant-supplied AuthenticationConfiguration; consumed only when `mode: CustomConfig`.                                                                                                                                                                                                                                                             | `object`   | `{}`   |
| `oidc.customConfig.config`         | Inline AuthenticationConfiguration YAML (`apiserver.config.k8s.io/v1beta1`). The chart writes the value verbatim into a Secret mounted at `/etc/kubernetes/authentication-config/config.yaml` on the kube-apiserver. Mutually exclusive with `secretRef.name`.                                                                                    | `string`   | `""`   |
| `oidc.customConfig.secretRef`      | Reference to an existing Secret in the tenant namespace carrying the `AuthenticationConfiguration`.                                                                                                                                                                                                                                               | `object`   | `{}`   |
| `oidc.customConfig.secretRef.name` | Name of an existing Secret in the tenant (release) namespace whose `config.yaml` key holds an `apiserver.config.k8s.io/v1beta1` `AuthenticationConfiguration`. Mutually exclusive with `customConfig.config`.                                                                                                                                     | `string`   | `""`   |
| `oidc.users`                       | Users granted access to the tenant cluster; each entry produces one ClusterRoleBinding inside the tenant cluster. Works for both `System` and `CustomConfig` modes.                                                                                                                                                                               | `[]object` | `[]`   |
| `oidc.users[i].email`              | Email address matched against the `email` claim from the issuer. Used verbatim as the `User:` subject in the ClusterRoleBinding inside the tenant cluster. The `email` claim is a built-in OIDC scope requested explicitly by the chart-generated `kubectl oidc-login` kubeconfig; every conformant OIDC provider (cozy realm included) emits it. | `string`   | `""`   |
| `oidc.users[i].role`               | Role to bind: `admin` maps to `ClusterRole/cluster-admin`, `view` maps to `ClusterRole/view`.                                                                                                                                                                                                                                                     | `string`   | `{}`   |


## Parameter examples and reference

### resources and resourcesPreset

`resources` sets explicit CPU and memory configurations for each replica.
When left empty, the preset defined in `resourcesPreset` is applied.

```yaml
resources:
  cpu: 4000m
  memory: 4Gi
```

`resourcesPreset` sets named CPU and memory configurations for each replica.
This setting is ignored if the corresponding `resources` value is set.

| Preset name | CPU    | memory  |
|-------------|--------|---------|
| `nano`      | `250m` | `128Mi` |
| `micro`     | `500m` | `256Mi` |
| `small`     | `1`    | `512Mi` |
| `medium`    | `1`    | `1Gi`   |
| `large`     | `2`    | `2Gi`   |
| `xlarge`    | `4`    | `4Gi`   |
| `2xlarge`   | `8`    | `8Gi`   |

### instanceType Resources

The following instanceType resources are provided by Cozystack:

| Name             | vCPUs | Memory |
|------------------|-------|--------|
| `cx1.2xlarge`    | 8     | 16Gi   |
| `cx1.2xlarge1gi` | 8     | 16Gi   |
| `cx1.4xlarge`    | 16    | 32Gi   |
| `cx1.4xlarge1gi` | 16    | 32Gi   |
| `cx1.8xlarge`    | 32    | 64Gi   |
| `cx1.8xlarge1gi` | 32    | 64Gi   |
| `cx1.large`      | 2     | 4Gi    |
| `cx1.large1gi`   | 2     | 4Gi    |
| `cx1.medium`     | 1     | 2Gi    |
| `cx1.medium1gi`  | 1     | 2Gi    |
| `cx1.xlarge`     | 4     | 8Gi    |
| `cx1.xlarge1gi`  | 4     | 8Gi    |
| `d1.2xlarge`     | 8     | 32Gi   |
| `d1.2xmedium`    | 2     | 4Gi    |
| `d1.4xlarge`     | 16    | 64Gi   |
| `d1.8xlarge`     | 32    | 128Gi  |
| `d1.large`       | 2     | 8Gi    |
| `d1.medium`      | 1     | 4Gi    |
| `d1.micro`       | 1     | 1Gi    |
| `d1.nano`        | 1     | 512Mi  |
| `d1.small`       | 1     | 2Gi    |
| `d1.xlarge`      | 4     | 16Gi   |
| `gn1.2xlarge`    | 8     | 32Gi   |
| `gn1.4xlarge`    | 16    | 64Gi   |
| `gn1.8xlarge`    | 32    | 128Gi  |
| `gn1.xlarge`     | 4     | 16Gi   |
| `m1.2xlarge`     | 8     | 64Gi   |
| `m1.2xlarge1gi`  | 8     | 64Gi   |
| `m1.4xlarge`     | 16    | 128Gi  |
| `m1.4xlarge1gi`  | 16    | 128Gi  |
| `m1.8xlarge`     | 32    | 256Gi  |
| `m1.8xlarge1gi`  | 32    | 256Gi  |
| `m1.large`       | 2     | 16Gi   |
| `m1.large1gi`    | 2     | 16Gi   |
| `m1.xlarge`      | 4     | 32Gi   |
| `m1.xlarge1gi`   | 4     | 32Gi   |
| `n1.2xlarge`     | 16    | 32Gi   |
| `n1.4xlarge`     | 32    | 64Gi   |
| `n1.8xlarge`     | 64    | 128Gi  |
| `n1.large`       | 4     | 8Gi    |
| `n1.medium`      | 4     | 4Gi    |
| `n1.xlarge`      | 8     | 16Gi   |
| `o1.2xlarge`     | 8     | 32Gi   |
| `o1.4xlarge`     | 16    | 64Gi   |
| `o1.8xlarge`     | 32    | 128Gi  |
| `o1.large`       | 2     | 8Gi    |
| `o1.medium`      | 1     | 4Gi    |
| `o1.micro`       | 1     | 1Gi    |
| `o1.nano`        | 1     | 512Mi  |
| `o1.small`       | 1     | 2Gi    |
| `o1.xlarge`      | 4     | 16Gi   |
| `rt1.2xlarge`    | 8     | 32Gi   |
| `rt1.4xlarge`    | 16    | 64Gi   |
| `rt1.8xlarge`    | 32    | 128Gi  |
| `rt1.large`      | 2     | 8Gi    |
| `rt1.medium`     | 1     | 4Gi    |
| `rt1.micro`      | 1     | 1Gi    |
| `rt1.small`      | 1     | 2Gi    |
| `rt1.xlarge`     | 4     | 16Gi   |
| `u1.2xlarge`     | 8     | 32Gi   |
| `u1.2xmedium`    | 2     | 4Gi    |
| `u1.4xlarge`     | 16    | 64Gi   |
| `u1.8xlarge`     | 32    | 128Gi  |
| `u1.large`       | 2     | 8Gi    |
| `u1.medium`      | 1     | 4Gi    |
| `u1.micro`       | 1     | 1Gi    |
| `u1.nano`        | 1     | 512Mi  |
| `u1.small`       | 1     | 2Gi    |
| `u1.xlarge`      | 4     | 16Gi   |

### Kubelet Resource Reservations

Each node group supports a `kubelet` object that configures how much memory and CPU the kubelet reserves for the host OS and the Kubernetes/system components running on the worker node.

When `systemReservedMemory` or `kubeReservedMemory` are left empty, they are **auto-computed** using the following formula:

1. Determine the **effective memory** of the node:
   - If `resources.memory` is explicitly set, use that value.
   - Otherwise, look up the `instanceType` and use its `memory.guest` value.
   - If neither is available, the reservation falls back to the minimum (256Mi).
2. Calculate **5%** of the effective memory (in MiB, rounded down).
3. **Clamp** the result to the range **[256Mi, 1Gi]**:
   - Nodes with 5 GiB or less get the minimum 256Mi reservation.
   - Nodes with 20 GiB or more get the maximum 1Gi reservation.

Both `systemReservedMemory` and `kubeReservedMemory` receive the same auto-computed value by default.

CPU reservations (`systemReservedCpu`, `kubeReservedCpu`) follow the same pattern: **5%** of effective CPU, clamped to **[50m, 500m]**. Both are auto-computed when left empty.

#### Kubelet Defaults

| Parameter | Default | Description |
| --- | --- | --- |
| `systemReservedMemory` | auto-computed | Memory reserved for host OS |
| `kubeReservedMemory` | auto-computed | Memory reserved for kubelet and container runtime |
| `systemReservedCpu` | auto-computed | CPU reserved for host OS |
| `kubeReservedCpu` | auto-computed | CPU reserved for kubelet and container runtime |
| `evictionHardMemory` | `7%` | Hard eviction threshold for memory |
| `evictionSoftMemory` | `10%` | Soft eviction threshold for memory |
| `evictionSoftGracePeriod` | `1m30s` *(hardcoded)* | Duration a soft eviction threshold must be breached before triggering eviction |
| `evictionMinimumReclaim` | `256Mi` *(hardcoded)* | Minimum amount of memory reclaimed per eviction action |

Eviction thresholds can be specified as percentages (e.g., `7%`) or absolute values (e.g., `200Mi`). Both thresholds must use the same unit type. The hard threshold must be strictly less than the soft threshold.

The `evictionSoftGracePeriod` and `evictionMinimumReclaim` parameters are currently hardcoded in the chart template and cannot be overridden through values.

#### Capacity Annotation

When kubelet resource reservations are configured, both the `capacity.cluster-autoscaler.kubernetes.io/memory` and `capacity.cluster-autoscaler.kubernetes.io/cpu` annotations on MachineDeployments report **allocatable** values instead of total node resources. Memory allocatable subtracts system-reserved, kube-reserved, and eviction-hard. CPU allocatable subtracts system-reserved and kube-reserved. This aligns the annotations with the values the cluster autoscaler uses in its scaling calculations.

Upgrading from a version without this feature may cause the autoscaler to see reduced per-node capacity after the annotations are updated, which can trigger additional scale-up operations. No action is typically required — the new values reflect the actual memory available for workload scheduling.

> **Note:** When neither `resources.memory` nor `instanceType` is set, eviction thresholds (default 7% hard / 10% soft) are still enforced by the kubelet at runtime, but the capacity annotation is not rendered. Without this annotation, the cluster-autoscaler has no visibility into these reservations and may over-schedule the node until evictions fire.

#### Example: Override kubelet reservations

```yaml
nodeGroups:
  md0:
    instanceType: "u1.large"
    kubelet:
      systemReservedMemory: "256Mi"
      kubeReservedMemory: "256Mi"
      evictionHardMemory: "500Mi"
      evictionSoftMemory: "1Gi"
```

### U Series: Universal

The U Series is quite neutral and provides resources for
general purpose applications.

*U* is the abbreviation for "Universal", hinting at the universal
attitude towards workloads.

VMs of instance types will share physical CPU cores on a
time-slice basis with other VMs.

#### U Series Characteristics

Specific characteristics of this series are:
- *Burstable CPU performance* - The workload has a baseline compute
  performance but is permitted to burst beyond this baseline, if
  excess compute resources are available.
- *vCPU-To-Memory Ratio (1:4)* - A vCPU-to-Memory ratio of 1:4, for less
  noise per node.

### O Series: Overcommitted

The O Series is based on the U Series, with the only difference
being that memory is overcommitted.

*O* is the abbreviation for "Overcommitted".

#### O Series Characteristics

Specific characteristics of this series are:
- *Burstable CPU performance* - The workload has a baseline compute
  performance but is permitted to burst beyond this baseline, if
  excess compute resources are available.
- *Overcommitted Memory* - Memory is over-committed in order to achieve
  a higher workload density.
- *vCPU-To-Memory Ratio (1:4)* - A vCPU-to-Memory ratio of 1:4, for less
  noise per node.

### CX Series: Compute Exclusive

The CX Series provides exclusive compute resources for compute
intensive applications.

*CX* is the abbreviation of "Compute Exclusive".

The exclusive resources are given to the compute threads of the
VM. In order to ensure this, some additional cores (depending
on the number of disks and NICs) will be requested to offload
the IO threading from cores dedicated to the workload.
In addition, in this series, the NUMA topology of the used
cores is provided to the VM.

#### CX Series Characteristics

Specific characteristics of this series are:
- *Hugepages* - Hugepages are used in order to improve memory
  performance.
- *Dedicated CPU* - Physical cores are exclusively assigned to every
  vCPU in order to provide fixed and high compute guarantees to the
  workload.
- *Isolated emulator threads* - Hypervisor emulator threads are isolated
  from the vCPUs in order to reduce emaulation related impact on the
  workload.
- *vNUMA* - Physical NUMA topology is reflected in the guest in order to
  optimize guest sided cache utilization.
- *vCPU-To-Memory Ratio (1:2)* - A vCPU-to-Memory ratio of 1:2.

### M Series: Memory

The M Series provides resources for memory intensive
applications.

*M* is the abbreviation of "Memory".

#### M Series Characteristics

Specific characteristics of this series are:
- *Hugepages* - Hugepages are used in order to improve memory
  performance.
- *Burstable CPU performance* - The workload has a baseline compute
  performance but is permitted to burst beyond this baseline, if
  excess compute resources are available.
- *vCPU-To-Memory Ratio (1:8)* - A vCPU-to-Memory ratio of 1:8, for much
  less noise per node.

### RT Series: RealTime

The RT Series provides resources for realtime applications, like Oslat.

*RT* is the abbreviation for "realtime".

This series of instance types requires nodes capable of running
realtime applications.

#### RT Series Characteristics

Specific characteristics of this series are:
- *Hugepages* - Hugepages are used in order to improve memory
  performance.
- *Dedicated CPU* - Physical cores are exclusively assigned to every
  vCPU in order to provide fixed and high compute guarantees to the
  workload.
- *Isolated emulator threads* - Hypervisor emulator threads are isolated
  from the vCPUs in order to reduce emaulation related impact on the
  workload.
- *vCPU-To-Memory Ratio (1:4)* - A vCPU-to-Memory ratio of 1:4 starting from
  the medium size.
