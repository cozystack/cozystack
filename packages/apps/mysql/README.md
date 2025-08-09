## Managed MariaDB Service

The Managed MariaDB Service offers a powerful and widely used relational database solution.
This service allows you to create and manage a replicated MariaDB cluster seamlessly.

## Deployment Details

This managed service is controlled by mariadb-operator, ensuring efficient management and seamless operation.

- Docs: https://mariadb.com/kb/en/documentation/
- GitHub: https://github.com/mariadb-operator/mariadb-operator

## HowTos

### How to switch master/slave replica

```
kubectl edit mariadb <instnace>
```
update:

```
spec:
  replication:
    primary:
      podIndex: 1
```

check status:

```
NAME        READY   STATUS    PRIMARY POD   AGE
<instance>  True    Running   app-db1-1     41d
```

### How to restore backup:

find snapshot:
```
restic -r s3:s3.example.org/mariadb-backups/database_name snapshots
```


restore:
```
restic -r s3:s3.example.org/mariadb-backups/database_name restore latest --target /tmp/
```

more details:
- https://blog.aenix.io/restic-effective-backup-from-stdin-4bc1e8f083c1

### Known issues

- **Replication can't not be finished with various errors**
- **Replication can't be finised in case if binlog purged**
  Until mariadbbackup is not used to bootstrap a node by mariadb-operator (this feature is not inmplemented yet), follow these manual steps to fix it:
  https://github.com/mariadb-operator/mariadb-operator/issues/141#issuecomment-1804760231

- **Corrupted indicies**
  Sometimes some indecies can be corrupted on master replica, you can recover them from slave:

  ```
  mysqldump -h <slave> -P 3306 -u<user> -p<password> --column-statistics=0 <database> <table> ~/tmp/fix-table.sql
  mysql -h <master> -P 3306 -u<user> -p<password> <database> < ~/tmp/fix-table.sql
  ```

## Parameters

### Common parameters

| Name               | Description                                                                                                                               | Type        | Value   |
| ------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- | ----------- | ------- |
| `replicas`         | Number of MariaDB replicas                                                                                                                | `int`       | `2`     |
| `resources`        | Explicit CPU and memory configuration for each MariaDB replica. When left empty, the preset defined in `resourcesPreset` is applied.      | `*object`   | `{}`    |
| `resources.cpu`    | CPU                                                                                                                                       | `*quantity` | `null`  |
| `resources.memory` | Memory                                                                                                                                    | `*quantity` | `null`  |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted. Allowed values: `nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`. | `string`    | `nano`  |
| `size`             | Persistent Volume Claim size, available for application data                                                                              | `quantity`  | `10Gi`  |
| `storageClass`     | StorageClass used to store the data                                                                                                       | `string`    | `""`    |
| `external`         | Enable external access from outside the cluster                                                                                           | `bool`      | `false` |


### Application-specific parameters

| Name                             | Description                             | Type                | Value   |
| -------------------------------- | --------------------------------------- | ------------------- | ------- |
| `users`                          | Users configuration                     | `map[string]object` | `{...}` |
| `users[name].password`           | Password for the user                   | `string`            | `""`    |
| `users[name].maxUserConnections` | Maximum amount of connections           | `int`               | `0`     |
| `databases`                      | Databases configuration                 | `map[string]object` | `{...}` |
| `databases[name].roles`          | Roles for the database                  | `*object`           | `null`  |
| `databases[name].roles.admin`    | List of users with admin privileges     | `[]string`          | `[]`    |
| `databases[name].roles.readonly` | List of users with read-only privileges | `[]string`          | `[]`    |


### Backup parameters

| Name                     | Description                                    | Type      | Value                                                  |
| ------------------------ | ---------------------------------------------- | --------- | ------------------------------------------------------ |
| `backup`                 | Backup configuration                           | `object`  | `{}`                                                   |
| `backup.enabled`         | Enable regular backups, default is `false`.    | `*bool`   | `false`                                                |
| `backup.s3Region`        | AWS S3 region where backups are stored         | `*string` | `us-east-1`                                            |
| `backup.s3Bucket`        | S3 bucket used for storing backups             | `*string` | `s3.example.org/postgres-backups`                      |
| `backup.schedule`        | Cron schedule for automated backups            | `*string` | `0 2 * * *`                                            |
| `backup.cleanupStrategy` | Retention strategy for cleaning up old backups | `*string` | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`     | Access key for S3, used for authentication     | `*string` | `oobaiRus9pah8PhohL1ThaeTa4UVa7gu`                     |
| `backup.s3SecretKey`     | Secret key for S3, used for authentication     | `*string` | `ju3eum4dekeich9ahM1te8waeGai0oog`                     |
| `backup.resticPassword`  | Password for Restic backup encryption          | `*string` | `ChaXoveekoh6eigh4siesheeda2quai0`                     |


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

### users

```yaml
users:
  user1:
    maxUserConnections: 1000
    password: hackme
  user2:
    maxUserConnections: 1000
    password: hackme
```


### databases

```yaml
databases:
  myapp1:
    roles:
      admin:
      - user1
      readonly:
      - user2
```
