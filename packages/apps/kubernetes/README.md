# Managed Kubernetes Service

## Managed Kubernetes in Cozystack

Whenever you want to deploy a custom containerized application in Cozystack, it's best to deploy it to a managed Kubernetes cluster.

Cozystack deploys and manages Kubernetes-as-a-service as standalone applications within each tenant’s isolated environment.
In Cozystack, such clusters are named tenant Kubernetes clusters, while the base Cozystack cluster is called a management or root cluster.
Tenant clusters are fully separated from the management cluster and are intended for deploying tenant-specific or customer-developed applications.

Within a tenant cluster, users can take advantage of LoadBalancer services and easily provision physical volumes as needed.                               
The control-plane operates within containers, while the worker nodes are deployed as virtual machines, all seamlessly managed by the application.

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

| Name                    | Description                                                                                                       | Value        |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------- | ------------ |
| `host`                  | Hostname used to access the Kubernetes cluster externally. Defaults to `<cluster-name>.<tenant-host>` when empty. | `""`         |
| `controlPlane.replicas` | Number of replicas for Kubernetes control-plane components.                                                       | `2`          |
| `storageClass`          | StorageClass used to store user data.                                                                             | `replicated` |
| `nodeGroups`            | nodeGroups configuration                                                                                          | `{}`         |

### Cluster Addons

| Name                                          | Description                                                                                                                                                                       | Value     |
| --------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------- |
| `addons.certManager.enabled`                  | Enable cert-manager, which automatically creates and manages SSL/TLS certificates.                                                                                                | `false`   |
| `addons.certManager.valuesOverride`           | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.cilium.valuesOverride`                | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.gatewayAPI.enabled`                   | Enable the Gateway API                                                                                                                                                            | `false`   |
| `addons.ingressNginx.enabled`                 | Enable the Ingress-NGINX controller (requires nodes labeled with the 'ingress-nginx' role).                                                                                       | `false`   |
| `addons.ingressNginx.valuesOverride`          | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.ingressNginx.exposeMethod`            | Method to expose the Ingress-NGINX controller. (allowed values: Proxied, LoadBalancer)                                                                                            | `Proxied` |
| `addons.ingressNginx.hosts`                   | List of domain names that the parent cluster should route to this tenant cluster. Taken into account only when `exposeMethod` is set to `Proxied`.                                | `[]`      |
| `addons.gpuOperator.enabled`                  | Enable the GPU-operator                                                                                                                                                           | `false`   |
| `addons.gpuOperator.valuesOverride`           | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.fluxcd.enabled`                       | Enable FluxCD                                                                                                                                                                     | `false`   |
| `addons.fluxcd.valuesOverride`                | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.monitoringAgents.enabled`             | Enable monitoring agents (Fluent Bit and VMAgents) to send logs and metrics. If tenant monitoring is enabled, data is sent to tenant storage; otherwise, it goes to root storage. | `false`   |
| `addons.monitoringAgents.valuesOverride`      | Custom values to override                                                                                                                                                         | `{}`      |
| `addons.verticalPodAutoscaler.valuesOverride` | Custom values to override                                                                                                                                                         | `{}`      |

### Kubernetes Control Plane Configuration

| Name                                               | Description                                                                                                                                      | Value    |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------ | -------- |
| `controlPlane.apiServer.resources`                 | Explicit CPU/memory resource requests and limits for the API server.                                                                             | `{}`     |
| `controlPlane.apiServer.resourcesPreset`           | Use a common resources preset when `resources` is not set explicitly. (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge) | `medium` |
| `controlPlane.controllerManager.resources`         | Explicit CPU/memory resource requests and limits for the controller manager.                                                                     | `{}`     |
| `controlPlane.controllerManager.resourcesPreset`   | Use a common resources preset when `resources` is not set explicitly. (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge) | `micro`  |
| `controlPlane.scheduler.resources`                 | Explicit CPU/memory resource requests and limits for the scheduler.                                                                              | `{}`     |
| `controlPlane.scheduler.resourcesPreset`           | Use a common resources preset when `resources` is not set explicitly. (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge) | `micro`  |
| `controlPlane.konnectivity.server.resources`       | Explicit CPU/memory resource requests and limits for the Konnectivity.                                                                           | `{}`     |
| `controlPlane.konnectivity.server.resourcesPreset` | Use a common resources preset when `resources` is not set explicitly. (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge) | `micro`  |

In production environments, it's recommended to set `resources` explicitly.
Example of `controlPlane.*.resources`:

```yaml
resources:
  limits:
    cpu: 4000m
    memory: 4Gi
  requests:
    cpu: 100m
    memory: 512Mi
```

Allowed values for `controlPlane.*.resourcesPreset` are `none`, `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`.
This value is ignored if the corresponding `resources` value is set. 

## Resources Reference

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

