# S3 bucket

## Parameters

### Parameters

| Name                   | Description                                                                                                                                                                                                       | Type                | Value   |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `locking`              | Provisions bucket from the `-lock` BucketClass (with object lock enabled).                                                                                                                                        | `bool`              | `false` |
| `storagePool`          | Selects a specific BucketClass by storage pool name.                                                                                                                                                              | `string`            | `""`    |
| `users`                | Users configuration map.                                                                                                                                                                                          | `map[string]object` | `{}`    |
| `users[name].readonly` | Whether the user has read-only access.                                                                                                                                                                            | `bool`              | `false` |
| `schedulingClass`      | Name of a SchedulingClass CR (cluster-scoped, group cozystack.io) applied to this application's workloads. When set, takes precedence over any tenant-level schedulingClass. Empty means inherit from the tenant. | `string`            | `""`    |

