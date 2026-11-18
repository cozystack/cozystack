# S3 bucket

## Parameters

### Parameters

| Name                   | Description                                                                                                                                                             | Type                | Value   |
| ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- | ------- |
| `locking`              | Provisions bucket from the `-lock` BucketClass (with object lock enabled).                                                                                              | `bool`              | `false` |
| `storagePool`          | Selects a specific BucketClass by storage pool name.                                                                                                                    | `string`            | `""`    |
| `ui`                   | Deploys the web UI (s3manager) with its Service and its Ingress or HTTPRoute at `<bucket>.<tenant host>`. Set to `false` to run the bucket without them.                | `bool`              | `true`  |
| `publicRead`           | Serves the bucket's objects to anyone without credentials at `<endpoint>/<bucket>/<key>`. Anonymous callers can read objects but cannot list the bucket or write to it. | `bool`              | `false` |
| `users`                | Users configuration map.                                                                                                                                                | `map[string]object` | `{}`    |
| `users[name].readonly` | Whether the user has read-only access.                                                                                                                                  | `bool`              | `false` |

