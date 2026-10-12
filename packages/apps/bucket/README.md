# S3 bucket

## Parameters

### Parameters

| Name                   | Description                                                                                                                                                                                                                                                                                   | Type                | Value   |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `locking`              | Provisions bucket from the `-lock` BucketClass (with object lock enabled).                                                                                                                                                                                                                    | `bool`              | `false` |
| `storagePool`          | Selects a specific BucketClass by storage pool name.                                                                                                                                                                                                                                          | `string`            | `""`    |
| `ui`                   | Deploys the web UI (s3manager) with its Service and its Ingress or HTTPRoute at `<bucket>.<tenant host>`. Set to `false` to run the bucket without them.                                                                                                                                      | `bool`              | `true`  |
| `users`                | Users configuration map.                                                                                                                                                                                                                                                                      | `map[string]object` | `{}`    |
| `users[name].readonly` | Whether the user has read-only access.                                                                                                                                                                                                                                                        | `bool`              | `false` |
| `schedulingClass`      | Name of a SchedulingClass CR (cluster-scoped, group cozystack.io) applied to this application's workloads in place of the tenant's. Must be the tenant's own class or one listed in the tenant's `allowedSchedulingClasses`; any other value is refused. Empty means inherit from the tenant. | `string`            | `""`    |

