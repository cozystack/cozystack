# SeaweedFS Database

## Parameters

### Database parameters

| Name              | Description                          | Type       | Value  |
| ----------------- | ------------------------------------ | ---------- | ------ |
| `db`              | Database configuration.              | `object`   | `{}`   |
| `db.replicas`     | Number of database replicas.         | `int`      | `2`    |
| `db.size`         | Persistent Volume size.              | `quantity` | `10Gi` |
| `db.storageClass` | StorageClass used to store the data. | `string`   | `""`   |

