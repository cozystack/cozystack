# Managed Kubernetes Service

## Managed Kubernetes in Cozystack

Whenever you want to deploy a custom containerized application in Cozystack, it's best to deploy it to a managed Kubernetes cluster.

Cozystack deploys and manages Kubernetes-as-a-service as standalone applications within each tenant’s isolated environment.
In Cozystack, such clusters are named tenant Kubernetes clusters, while the base Cozystack cluster is called a management or root cluster.
Tenant clusters are fully separated from the management cluster and are intended for deploying tenant-specific or customer-developed applications.

Within a tenant cluster, users can take advantage of LoadBalancer services and easily provision physical volumes as needed.                               
The control-plane operates within containers, while the worker nodes are deployed as virtual machines, all seamlessly managed by the application.

Kubernetes version in tenant clusters is independent of Kubernetes in the management cluster.
Users can select the latest patch versions from 1.28 to 1.33.

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

-   **Etcd Cluster**: A dedicated etcd cluster is deployed using Ænix's [etcd-operator](https://github.com/aenix-io/etcd-operator).
    It provides reliable and scalable key-value storage for the Kubernetes control plane.

-   **Worker Nodes**: Virtual Machines are provisioned to serve as worker nodes using KubeVirt.
    These nodes are configured to join the tenant Kubernetes cluster, enabling the deployment and management of workloads.

-   **Cluster API**: Cozystack is using the [Kubernetes Cluster API](https://cluster-api.sigs.k8s.io/) to provision the components of a cluster.

This architecture ensures isolated, scalable, and efficient tenant Kubernetes environments.

See the reference for components utilized in this service:

- [Kamaji Control Plane](https://kamaji.clastix.io)
- [Kamaji — Cluster API](https://kamaji.clastix.io/cluster-api/)
- [github.com/clastix/kamaji](https://github.com/clastix/kamaji)
- [KubeVirt](https://kubevirt.io/)
- [github.com/kubevirt/kubevirt](https://github.com/kubevirt/kubevirt)
- [github.com/aenix-io/etcd-operator](https://github.com/aenix-io/etcd-operator)
- [Kubernetes Cluster API](https://cluster-api.sigs.k8s.io/)
- [github.com/kubernetes-sigs/cluster-api-provider-kubevirt](https://github.com/kubernetes-sigs/cluster-api-provider-kubevirt)
- [github.com/kubevirt/csi-driver](https://github.com/kubevirt/csi-driver)

## Parameters

### Common Parameters

| Name           | Description                          | Type     | Value        |
| -------------- | ------------------------------------ | -------- | ------------ |
| `storageClass` | StorageClass used to store the data. | `string` | `replicated` |


### Application-specific Parameters

| Name                                | Description                                                                                    | Type                | Value       |
| ----------------------------------- | ---------------------------------------------------------------------------------------------- | ------------------- | ----------- |
| `nodeGroups`                        | Worker nodes configuration map.                                                                | `map[string]object` | `{...}`     |
| `nodeGroups[name].minReplicas`      | Minimum number of replicas.                                                                    | `int`               | `0`         |
| `nodeGroups[name].maxReplicas`      | Maximum number of replicas.                                                                    | `int`               | `10`        |
| `nodeGroups[name].instanceType`     | Virtual machine instance type.                                                                 | `string`            | `u1.medium` |
| `nodeGroups[name].ephemeralStorage` | Ephemeral storage size.                                                                        | `quantity`          | `20Gi`      |
| `nodeGroups[name].roles`            | List of node roles.                                                                            | `[]string`          | `[]`        |
| `nodeGroups[name].resources`        | CPU and memory resources for each worker node.                                                 | `object`            | `{}`        |
| `nodeGroups[name].resources.cpu`    | CPU available.                                                                                 | `quantity`          | `""`        |
| `nodeGroups[name].resources.memory` | Memory (RAM) available.                                                                        | `quantity`          | `""`        |
| `nodeGroups[name].gpus`             | List of GPUs to attach (NVIDIA driver requires at least 4 GiB RAM).                            | `[]object`          | `[]`        |
| `nodeGroups[name].gpus[i].name`     | Name of GPU, such as "nvidia.com/AD102GL_L40S".                                                | `string`            | `""`        |
| `version`                           | Kubernetes version (vMAJOR.MINOR). Supported: 1.28–1.33.                                     | `string`            | `v1.33`     |
| `host`                              | External hostname for Kubernetes cluster. Defaults to `<cluster-name>.<tenant-host>` if empty. | `string`            | `""`        |


### Cluster Addons

| Name                                          | Description                                                                 | Type       | Value     |
| --------------------------------------------- | --------------------------------------------------------------------------- | ---------- | --------- |
| `addons`                                      | Cluster addons configuration.                                               | `object`   | `{}`      |
| `addons.certManager`                          | Cert-manager addon.                                                         | `object`   | `{}`      |
| `addons.certManager.enabled`                  | Enable cert-manager.                                                        | `bool`     | `false`   |
| `addons.certManager.valuesOverride`           | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.cilium`                               | Cilium CNI plugin.                                                          | `object`   | `{}`      |
| `addons.cilium.valuesOverride`                | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.gatewayAPI`                           | Gateway API addon.                                                          | `object`   | `{}`      |
| `addons.gatewayAPI.enabled`                   | Enable Gateway API.                                                         | `bool`     | `false`   |
| `addons.ingressNginx`                         | Ingress-NGINX controller.                                                   | `object`   | `{}`      |
| `addons.ingressNginx.enabled`                 | Enable the controller (requires nodes labeled `ingress-nginx`).             | `bool`     | `false`   |
| `addons.ingressNginx.exposeMethod`            | Method to expose the controller. Allowed values: `Proxied`, `LoadBalancer`. | `string`   | `Proxied` |
| `addons.ingressNginx.hosts`                   | Domains routed to this tenant cluster when `exposeMethod` is `Proxied`.     | `[]string` | `[]`      |
| `addons.ingressNginx.valuesOverride`          | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.gpuOperator`                          | NVIDIA GPU Operator.                                                        | `object`   | `{}`      |
| `addons.gpuOperator.enabled`                  | Enable GPU Operator.                                                        | `bool`     | `false`   |
| `addons.gpuOperator.valuesOverride`           | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.fluxcd`                               | FluxCD GitOps operator.                                                     | `object`   | `{}`      |
| `addons.fluxcd.enabled`                       | Enable FluxCD.                                                              | `bool`     | `false`   |
| `addons.fluxcd.valuesOverride`                | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.monitoringAgents`                     | Monitoring agents.                                                          | `object`   | `{}`      |
| `addons.monitoringAgents.enabled`             | Enable monitoring agents.                                                   | `bool`     | `false`   |
| `addons.monitoringAgents.valuesOverride`      | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.verticalPodAutoscaler`                | Vertical Pod Autoscaler.                                                    | `object`   | `{}`      |
| `addons.verticalPodAutoscaler.valuesOverride` | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.velero`                               | Velero backup/restore addon.                                                | `object`   | `{}`      |
| `addons.velero.enabled`                       | Enable Velero.                                                              | `bool`     | `false`   |
| `addons.velero.valuesOverride`                | Custom Helm values overrides.                                               | `object`   | `{}`      |
| `addons.coredns`                              | CoreDNS addon.                                                              | `object`   | `{}`      |
| `addons.coredns.valuesOverride`               | Custom Helm values overrides.                                               | `object`   | `{}`      |


### Kubernetes Control Plane Configuration

| Name                                                | Description                                      | Type       | Value    |
| --------------------------------------------------- | ------------------------------------------------ | ---------- | -------- |
| `controlPlane`                                      | Kubernetes control-plane configuration.          | `object`   | `{}`     |
| `controlPlane.replicas`                             | Number of control-plane replicas.                | `int`      | `2`      |
| `controlPlane.apiServer`                            | API Server configuration.                        | `object`   | `{}`     |
| `controlPlane.apiServer.resources`                  | CPU and memory resources for API Server.         | `object`   | `{}`     |
| `controlPlane.apiServer.resources.cpu`              | CPU available.                                   | `quantity` | `""`     |
| `controlPlane.apiServer.resources.memory`           | Memory (RAM) available.                          | `quantity` | `""`     |
| `controlPlane.apiServer.resourcesPreset`            | Preset if `resources` omitted.                   | `string`   | `medium` |
| `controlPlane.controllerManager`                    | Controller Manager configuration.                | `object`   | `{}`     |
| `controlPlane.controllerManager.resources`          | CPU and memory resources for Controller Manager. | `object`   | `{}`     |
| `controlPlane.controllerManager.resources.cpu`      | CPU available.                                   | `quantity` | `""`     |
| `controlPlane.controllerManager.resources.memory`   | Memory (RAM) available.                          | `quantity` | `""`     |
| `controlPlane.controllerManager.resourcesPreset`    | Preset if `resources` omitted.                   | `string`   | `micro`  |
| `controlPlane.scheduler`                            | Scheduler configuration.                         | `object`   | `{}`     |
| `controlPlane.scheduler.resources`                  | CPU and memory resources for Scheduler.          | `object`   | `{}`     |
| `controlPlane.scheduler.resources.cpu`              | CPU available.                                   | `quantity` | `""`     |
| `controlPlane.scheduler.resources.memory`           | Memory (RAM) available.                          | `quantity` | `""`     |
| `controlPlane.scheduler.resourcesPreset`            | Preset if `resources` omitted.                   | `string`   | `micro`  |
| `controlPlane.konnectivity`                         | Konnectivity configuration.                      | `object`   | `{}`     |
| `controlPlane.konnectivity.server`                  | Konnectivity Server configuration.               | `object`   | `{}`     |
| `controlPlane.konnectivity.server.resources`        | CPU and memory resources for Konnectivity.       | `object`   | `{}`     |
| `controlPlane.konnectivity.server.resources.cpu`    | CPU available.                                   | `quantity` | `""`     |
| `controlPlane.konnectivity.server.resources.memory` | Memory (RAM) available.                          | `quantity` | `""`     |
| `controlPlane.konnectivity.server.resourcesPreset`  | Preset if `resources` omitted.                   | `string`   | `micro`  |


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

| Name          | vCPUs | Memory |
|---------------|-------|--------|
| `cx1.2xlarge` | 8     | 16Gi   |
| `cx1.4xlarge` | 16    | 32Gi   |
| `cx1.8xlarge` | 32    | 64Gi   |
| `cx1.large`   | 2     | 4Gi    |
| `cx1.medium`  | 1     | 2Gi    |
| `cx1.xlarge`  | 4     | 8Gi    |
| `gn1.2xlarge` | 8     | 32Gi   |
| `gn1.4xlarge` | 16    | 64Gi   |
| `gn1.8xlarge` | 32    | 128Gi  |
| `gn1.xlarge`  | 4     | 16Gi   |
| `m1.2xlarge`  | 8     | 64Gi   |
| `m1.4xlarge`  | 16    | 128Gi  |
| `m1.8xlarge`  | 32    | 256Gi  |
| `m1.large`    | 2     | 16Gi   |
| `m1.xlarge`   | 4     | 32Gi   |
| `n1.2xlarge`  | 16    | 32Gi   |
| `n1.4xlarge`  | 32    | 64Gi   |
| `n1.8xlarge`  | 64    | 128Gi  |
| `n1.large`    | 4     | 8Gi    |
| `n1.medium`   | 4     | 4Gi    |
| `n1.xlarge`   | 8     | 16Gi   |
| `o1.2xlarge`  | 8     | 32Gi   |
| `o1.4xlarge`  | 16    | 64Gi   |
| `o1.8xlarge`  | 32    | 128Gi  |
| `o1.large`    | 2     | 8Gi    |
| `o1.medium`   | 1     | 4Gi    |
| `o1.micro`    | 1     | 1Gi    |
| `o1.nano`     | 1     | 512Mi  |
| `o1.small`    | 1     | 2Gi    |
| `o1.xlarge`   | 4     | 16Gi   |
| `rt1.2xlarge` | 8     | 32Gi   |
| `rt1.4xlarge` | 16    | 64Gi   |
| `rt1.8xlarge` | 32    | 128Gi  |
| `rt1.large`   | 2     | 8Gi    |
| `rt1.medium`  | 1     | 4Gi    |
| `rt1.micro`   | 1     | 1Gi    |
| `rt1.small`   | 1     | 2Gi    |
| `rt1.xlarge`  | 4     | 16Gi   |
| `u1.2xlarge`  | 8     | 32Gi   |
| `u1.2xmedium` | 2     | 4Gi    |
| `u1.4xlarge`  | 16    | 64Gi   |
| `u1.8xlarge`  | 32    | 128Gi  |
| `u1.large`    | 2     | 8Gi    |
| `u1.medium`   | 1     | 4Gi    |
| `u1.micro`    | 1     | 1Gi    |
| `u1.nano`     | 1     | 512Mi  |
| `u1.small`    | 1     | 2Gi    |
| `u1.xlarge`   | 4     | 16Gi   |

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
