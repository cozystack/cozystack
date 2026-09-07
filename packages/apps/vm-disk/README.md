# Virtual Machine Disk

A Virtual Machine Disk

> `storageClass` and `source` are annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it. `DataVolume.spec` is immutable in CDI, so the chart also fails the release on an attempted `storageClass`/`source` edit of an existing disk rather than silently reusing the old spec; delete and recreate the disk to change either field.

> `bindImmediately` is on by default, so a disk's volume is provisioned when the disk is created rather than deferred to the first consumer. That default exists because a VMDisk is a standalone object with a lifecycle of its own: on a `WaitForFirstConsumer` StorageClass a disk that no VM has attached yet would otherwise never populate at all. It costs something on a node-pinned class such as `local`, where the CDI worker pod becomes the first consumer and the volume lands where that pod was scheduled rather than where the VM will run, so a VM whose placement is constrained (a `nodeSelector`, a resource request only one node satisfies, or the Windows affinity applied by `_cluster.scheduling.dedicatedNodesForWindowsVMs`) can stay unschedulable against a node-affinity conflict, and `storageClass` is immutable so the only exit is deleting and recreating the disk. Turn `bindImmediately` off for a disk a VMInstance will consume on such a class: KubeVirt then renders a temporary launcher pod carrying the VM's own affinity and node selector, and the volume binds where the VM can actually run. The chart default `replicated` binds `Immediate` regardless, so neither setting changes anything there. One consequence to expect with it off on a `WaitForFirstConsumer` class: the disk reports ready while it still holds no data, because waiting for a consumer is where such a disk is supposed to rest — and a disk created with `source.upload` reports ready the same way while it waits for `virtctl image-upload`.

## Parameters

### Common parameters

| Name                | Description                                                              | Type       | Value        |
| ------------------- | ------------------------------------------------------------------------ | ---------- | ------------ |
| `source`            | The source image location used to create a disk.                         | `object`   | `{}`         |
| `source.image`      | Use image by name from default collection.                               | `*object`  | `null`       |
| `source.image.name` | Name of the image to use.                                                | `string`   | `""`         |
| `source.upload`     | Upload local image.                                                      | `*object`  | `null`       |
| `source.http`       | Download image from an HTTP source.                                      | `*object`  | `null`       |
| `source.http.url`   | URL to download the image.                                               | `string`   | `""`         |
| `source.disk`       | Clone an existing vm-disk.                                               | `*object`  | `null`       |
| `source.disk.name`  | Name of the vm-disk to clone.                                            | `string`   | `""`         |
| `optical`           | Defines if disk should be considered optical.                            | `bool`     | `false`      |
| `storage`           | The size of the disk allocated for the virtual machine.                  | `quantity` | `5Gi`        |
| `storageClass`      | StorageClass used to store the data.                                     | `string`   | `replicated` |
| `bindImmediately`   | Provision the volume at disk creation instead of waiting for a consumer. | `bool`     | `true`       |

