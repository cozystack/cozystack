# Virtual Machine Disk

A Virtual Machine Disk

> `storageClass` and `source` are annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it. `DataVolume.spec` is immutable in CDI, so the chart also fails the release on an attempted `storageClass`/`source` edit of an existing disk rather than silently reusing the old spec; delete and recreate the disk to change either field.

> Every disk requests immediate binding (`cdi.kubevirt.io/storage.bind.immediate.requested`), so its volume is provisioned when the disk is created rather than deferred to the first VM that consumes it. That is deliberate: a VMDisk is a standalone object with a lifecycle of its own, and on a `WaitForFirstConsumer` StorageClass a disk that no VM has attached yet would otherwise never populate at all. The consequence on a node-pinned class such as `local` is that the CDI worker pod picks the node, so the volume lands where that pod was scheduled rather than where the VM will run: a VM whose placement is constrained — a `nodeSelector`, a resource request only one node satisfies, or the Windows affinity applied by `_cluster.scheduling.dedicatedNodesForWindowsVMs` — can then stay unschedulable against a node-affinity conflict, and because `storageClass` is immutable the only exit is deleting and recreating the disk. The chart default `replicated` binds `Immediate` regardless and is unaffected. Node-pinned classes are the case to weigh before using them for VM disks.

## Parameters

### Common parameters

| Name                | Description                                             | Type       | Value        |
| ------------------- | ------------------------------------------------------- | ---------- | ------------ |
| `source`            | The source image location used to create a disk.        | `object`   | `{}`         |
| `source.image`      | Use image by name from default collection.              | `*object`  | `null`       |
| `source.image.name` | Name of the image to use.                               | `string`   | `""`         |
| `source.upload`     | Upload local image.                                     | `*object`  | `null`       |
| `source.http`       | Download image from an HTTP source.                     | `*object`  | `null`       |
| `source.http.url`   | URL to download the image.                              | `string`   | `""`         |
| `source.disk`       | Clone an existing vm-disk.                              | `*object`  | `null`       |
| `source.disk.name`  | Name of the vm-disk to clone.                           | `string`   | `""`         |
| `optical`           | Defines if disk should be considered optical.           | `bool`     | `false`      |
| `storage`           | The size of the disk allocated for the virtual machine. | `quantity` | `5Gi`        |
| `storageClass`      | StorageClass used to store the data.                    | `string`   | `replicated` |

