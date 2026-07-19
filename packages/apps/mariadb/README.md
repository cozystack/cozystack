## Managed MariaDB Service

The Managed MariaDB Service offers a powerful and widely used relational database solution.
This service allows you to create and manage a replicated MariaDB cluster seamlessly.

## Deployment Details

This managed service is controlled by mariadb-operator, ensuring efficient management and seamless operation.

- Docs: https://mariadb.com/kb/en/documentation/
- GitHub: https://github.com/mariadb-operator/mariadb-operator

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## HowTos

### How to switch master/slave replica

```bash
kubectl edit mariadb <instance>
```
update:

```bash
spec:
  replication:
    primary:
      podIndex: 1
```

check status:

```bash
NAME        READY   STATUS    PRIMARY POD   AGE
<instance>  True    Running   app-db1-1     41d
```

### How to back up and restore a MariaDB application

The recommended path is the Cozystack `BackupClass` / `Plan` /
`RestoreJob` flow with the operator-native `strategy.backups.cozystack.io/MariaDB`
strategy. See [examples/backups/mariadb](../../../examples/backups/mariadb/)
for a numbered, end-to-end walkthrough (bucket, source, strategy,
BackupClass, Plan, ad-hoc BackupJob, and both in-place + to-copy
RestoreJob fixtures).

The chart's `backup.*` block (mariadb-dump + restic CronJob) is
**deprecated** and remains supported for backward compatibility only.
Existing tenants can keep using it unchanged; new deployments should
use the operator-native flow above.

#### How to restore from a deprecated restic-based backup

find snapshot:
```bash
restic -r s3:s3.example.org/mariadb-backups/database_name snapshots
```


restore:
```bash
restic -r s3:s3.example.org/mariadb-backups/database_name restore latest --target /tmp/
```

more details:
- https://blog.aenix.io/restic-effective-backup-from-stdin-4bc1e8f083c1

### How to connect over TLS

Instances created by this chart always serve TLS. The chart asks the operator for it in every configuration, and an instance that omits the setting gets TLS from the operator's own defaults anyway, so there has never been a plaintext-only MariaDB here. What `tls.enabled` selects is whether the instance gets a *managed* TLS setup — its own CA and server certificate issued through cert-manager, plus enforcement — rather than the operator's CA with no enforcement. It is managed automatically when `external` is `true`, and can be turned on independently by setting `tls.enabled: true`.

Whenever TLS is managed, `tls.required` defaults to `true`, which sets `require_secure_transport=ON` on the server: plaintext connections are refused. That enforcement, not the presence of TLS, is what existing clients notice. When TLS is unmanaged, enforcement is left to the operator, which does not enforce — so `tls.required` has no effect there.

Certificates are issued by a per-instance CA managed by cert-manager. The server certificate covers the instance services (`<instance>`, `<instance>-primary`, `<instance>-secondary`), the headless service used for per-pod routing, `localhost`, and — when `external` is `true` — the external hostname.

Connect with the MariaDB client by supplying the CA and asking for full verification:

```bash
mysql -h <instance> -u <user> -p<password> --ssl-ca=/path/to/ca.crt --ssl-verify-server-cert
```

`--ssl-ca` alone establishes trust but leaves the hostname unchecked; `--ssl-verify-server-cert` is what validates the server name against the certificate. Note that these are MariaDB client options. The MySQL 8 client spells the same intent as `--ssl-mode=VERIFY_IDENTITY`, which the MariaDB client does not accept.

The trust anchor is available in the instance namespace as the Secret `<instance>-ca-bundle`, under the key `ca.crt`. It is reconciled by the operator whenever TLS is enabled, contains no private key, and follows CA rollovers, so it is the certificate to distribute to clients:

```bash
kubectl get secret <instance>-ca-bundle -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
```

Mounting that Secret into a client Pod is the usual in-cluster approach; nothing needs to be copied out of the namespace.

> External clients additionally need a DNS record pointing at the LoadBalancer address. The certificate carries `<instance>.<host>` as a SAN but no IP SAN, so connecting to the address directly fails verification.

#### Migrating an existing instance

Managing TLS on an instance that already has clients is a breaking change — not because TLS appears, it was always there, but because `tls.required: true` starts refusing plaintext connections as soon as the change is applied. This affects existing instances with `external: true`, because they gain enforcement automatically. The CA also changes hands, from the operator's to the one issued for this instance; a client that pinned the operator's `ca.crt` needs to re-read it, and `<instance>-ca-bundle` carries both across the switch.

To migrate without downtime, take enforcement in a second step, once clients are updated:

```yaml
tls:
  enabled: true
  required: false
```

Move clients over to TLS one at a time, then drop `required: false` to enforce it.

### Known issues

- **Replication can't be finished with various errors**
- **Replication can't be finished in case if `binlog` purged**

  Until `mariadbbackup` is not used to bootstrap a node by mariadb-operator (this feature is not implemented yet), follow these manual steps to fix it:
  https://github.com/mariadb-operator/mariadb-operator/issues/141#issuecomment-1804760231

- **Corrupted indices**
  Sometimes some indices can be corrupted on master replica, you can recover them from slave:

  ```bash
  mysqldump -h <slave> -P 3306 -u<user> -p<password> --column-statistics=0 <database> <table> ~/tmp/fix-table.sql
  mysql -h <master> -P 3306 -u<user> -p<password> <database> < ~/tmp/fix-table.sql
  ```

  When TLS is enforced (`tls.required`) the server refuses plaintext connections, so add `--ssl-ca=/path/to/ca.crt --ssl-verify-server-cert` to both commands.

## Parameters

### Common parameters

| Name               | Description                                                                                                                       | Type       | Value     |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of MariaDB replicas.                                                                                                       | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each MariaDB replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                    | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                           | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                           | `string`   | `t1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                      | `quantity` | `10Gi`    |
| `storageClass`     | StorageClass used to store the data.                                                                                              | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                  | `bool`     | `false`   |


### TLS parameters

| Name           | Description                                                                                                                                                                                                                                                                                                                                                                                 | Type     | Value   |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------- |
| `tls`          | TLS configuration. The chart always asks the operator to serve TLS, so these settings select CA ownership and enforcement rather than whether TLS exists. Managed automatically when `external` is true.                                                                                                                                                                                    | `object` | `{}`    |
| `tls.enabled`  | Manage TLS for this instance: issue a dedicated CA and server certificate through cert-manager, and enforce TLS by default. This does not switch TLS on: the chart always asks the operator to serve TLS, so what this selects is whether the instance gets its own CA and enforcement instead of the operator's CA with no enforcement. When omitted, defaults to the value of `external`. | `*bool`  | `null`  |
| `tls.required` | Enforce TLS for all connections (sets MariaDB require_secure_transport=ON). Applies only when TLS is managed, where it defaults to true; when TLS is unmanaged, enforcement is left to the operator, which does not enforce. Set to false only during migration when legacy clients cannot use TLS yet.                                                                                     | `bool`   | `false` |


### Version parameters

| Name      | Description                           | Type     | Value   |
| --------- | ------------------------------------- | -------- | ------- |
| `version` | MariaDB major.minor version to deploy | `string` | `v11.8` |


### Application-specific parameters

| Name                             | Description                              | Type                | Value |
| -------------------------------- | ---------------------------------------- | ------------------- | ----- |
| `users`                          | Users configuration map.                 | `map[string]object` | `{}`  |
| `users[name].password`           | Password for the user.                   | `string`            | `""`  |
| `users[name].maxUserConnections` | Maximum number of connections.           | `int`               | `0`   |
| `databases`                      | Databases configuration map.             | `map[string]object` | `{}`  |
| `databases[name].roles`          | Roles assigned to users.                 | `object`            | `{}`  |
| `databases[name].roles.admin`    | List of users with admin privileges.     | `[]string`          | `[]`  |
| `databases[name].roles.readonly` | List of users with read-only privileges. | `[]string`          | `[]`  |


### Backup parameters (DEPRECATED)

| Name                     | Description                                                                                           | Type     | Value                                                  |
| ------------------------ | ----------------------------------------------------------------------------------------------------- | -------- | ------------------------------------------------------ |
| `backup`                 | DEPRECATED: Backup configuration. Prefer the BackupClass / Plan flow under examples/backups/mariadb/. | `object` | `{}`                                                   |
| `backup.enabled`         | DEPRECATED: Enable regular backups (default: false).                                                  | `bool`   | `false`                                                |
| `backup.s3Region`        | DEPRECATED: AWS S3 region where backups are stored.                                                   | `string` | `us-east-1`                                            |
| `backup.s3Bucket`        | DEPRECATED: S3 bucket used for storing backups.                                                       | `string` | `s3.example.org/mariadb-backups`                       |
| `backup.schedule`        | DEPRECATED: Cron schedule for automated backups.                                                      | `string` | `0 2 * * *`                                            |
| `backup.cleanupStrategy` | DEPRECATED: Retention strategy for cleaning up old backups.                                           | `string` | `--keep-last=3 --keep-daily=3 --keep-within-weekly=1m` |
| `backup.s3AccessKey`     | DEPRECATED: Access key for S3 authentication.                                                         | `string` | `<your-access-key>`                                    |
| `backup.s3SecretKey`     | DEPRECATED: Secret key for S3 authentication.                                                         | `string` | `<your-secret-key>`                                    |
| `backup.resticPassword`  | DEPRECATED: Password for Restic backup encryption.                                                    | `string` | `<password>`                                           |


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

Presets follow a cloud-style `<series>.<size>` naming convention. Five series cover the full CPU-to-memory ratio range (`t1` 1:0.5, `c1` 1:1, `s1` 1:2, `u1` 1:4, `m1` 1:8) and each series ships eight sizes (`nano` through `4xlarge`). The legacy flat names (`nano`, `micro`, `small`, `medium`, `large`, `xlarge`, `2xlarge`) remain accepted as deprecated aliases of their 1:1 instance-type equivalents.

See [`docs/operations/resource-presets.md`](../../../docs/operations/resource-presets.md) for the full size matrix and the legacy-to-instance-type mapping.

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
