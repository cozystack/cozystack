# Managed MongoDB Service

MongoDB is a popular document-oriented NoSQL database known for its flexibility and scalability.
The Managed MongoDB Service provides a self-healing replicated cluster managed by the Percona Operator for MongoDB.

## Deployment Details

This managed service is controlled by the Percona Operator for MongoDB, ensuring efficient management and seamless operation.

- Docs: <https://docs.percona.com/percona-operator-for-mongodb/>
- Github: <https://github.com/percona/percona-server-mongodb-operator>

## Deployment Modes

### Replica Set Mode (default)

By default, MongoDB deploys as a replica set with the specified number of replicas.
This mode is suitable for most use cases requiring high availability.

### Sharded Cluster Mode

Enable `sharding: true` for horizontal scaling across multiple shards.
Each shard is a replica set, and mongos routers handle query routing.

## Notes

### External Access

When `external: true` is enabled:
- **Replica Set mode**: Traffic is load-balanced across all replica set members. This works well for read operations, but write operations require connecting to the primary. MongoDB drivers handle primary discovery automatically using the replica set connection string.
- **Sharded mode**: Traffic is routed through mongos routers, which handle both reads and writes correctly.

### Credentials

On first install, the credentials secret will be empty until the Percona operator initializes the cluster.
Run `helm upgrade` after MongoDB is ready to populate the credentials secret with the actual password.

## Parameters

### Common parameters

| Name               | Description                                                                                                                       | Type       | Value   |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`         | Number of MongoDB replicas in replica set.                                                                                        | `int`      | `3`     |
| `resources`        | Explicit CPU and memory configuration for each MongoDB replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                    | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                           | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                           | `string`   | `small` |
| `size`             | Persistent Volume Claim size available for application data.                                                                      | `quantity` | `10Gi`  |
| `storageClass`     | StorageClass used to store the data.                                                                                              | `string`   | `""`    |
| `external`         | Enable external access from outside the cluster.                                                                                  | `bool`     | `false` |
| `version`          | MongoDB major version to deploy.                                                                                                  | `string`   | `v8`    |


### Sharding configuration

| Name                                | Description                                                        | Type       | Value   |
| ----------------------------------- | ------------------------------------------------------------------ | ---------- | ------- |
| `sharding`                          | Enable sharded cluster mode. When disabled, deploys a replica set. | `bool`     | `false` |
| `shardingConfig`                    | Configuration for sharded cluster mode.                            | `object`   | `{}`    |
| `shardingConfig.configServers`      | Number of config server replicas.                                  | `int`      | `3`     |
| `shardingConfig.configServerSize`   | PVC size for config servers.                                       | `quantity` | `3Gi`   |
| `shardingConfig.mongos`             | Number of mongos router replicas.                                  | `int`      | `2`     |
| `shardingConfig.shards`             | List of shard configurations.                                      | `[]object` | `[...]` |
| `shardingConfig.shards[i].name`     | Shard name.                                                        | `string`   | `""`    |
| `shardingConfig.shards[i].replicas` | Number of replicas in this shard.                                  | `int`      | `0`     |
| `shardingConfig.shards[i].size`     | PVC size for this shard.                                           | `quantity` | `""`    |


### Users configuration

| Name                        | Description                                         | Type                | Value |
| --------------------------- | --------------------------------------------------- | ------------------- | ----- |
| `users`                     | Custom MongoDB users configuration map.             | `map[string]object` | `{}`  |
| `users[name].password`      | Password for the user (auto-generated if omitted).  | `string`            | `""`  |
| `users[name].db`            | Database to authenticate against.                   | `string`            | `""`  |
| `users[name].roles`         | List of MongoDB roles with database scope.          | `[]object`          | `[]`  |
| `users[name].roles[i].name` | Role name (e.g., readWrite, dbAdmin, clusterAdmin). | `string`            | `""`  |
| `users[name].roles[i].db`   | Database the role applies to.                       | `string`            | `""`  |


### Backup parameters

| Name                     | Description                                            | Type     | Value                               |
| ------------------------ | ------------------------------------------------------ | -------- | ----------------------------------- |
| `backup`                 | Backup configuration.                                  | `object` | `{}`                                |
| `backup.enabled`         | Enable regular backups.                                | `bool`   | `false`                             |
| `backup.schedule`        | Cron schedule for automated backups.                   | `string` | `0 2 * * *`                         |
| `backup.retentionPolicy` | Retention policy (e.g. "30d").                         | `string` | `30d`                               |
| `backup.destinationPath` | Destination path for backups (e.g. s3://bucket/path/). | `string` | `s3://bucket/path/to/folder/`       |
| `backup.endpointURL`     | S3 endpoint URL for uploads.                           | `string` | `http://minio-gateway-service:9000` |
| `backup.s3AccessKey`     | Access key for S3 authentication.                      | `string` | `""`                                |
| `backup.s3SecretKey`     | Secret key for S3 authentication.                      | `string` | `""`                                |


### Bootstrap (recovery) parameters

| Name                     | Description                                               | Type     | Value   |
| ------------------------ | --------------------------------------------------------- | -------- | ------- |
| `bootstrap`              | Bootstrap configuration.                                  | `object` | `{}`    |
| `bootstrap.enabled`      | Whether to restore from a backup.                         | `bool`   | `false` |
| `bootstrap.recoveryTime` | Timestamp for point-in-time recovery; empty means latest. | `string` | `""`    |
| `bootstrap.backupName`   | Name of backup to restore from.                           | `string` | `""`    |

