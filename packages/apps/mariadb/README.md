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

Instances created by this chart always serve TLS. The chart asks the operator for it in every configuration, and an instance that omits the setting gets TLS from the operator's own defaults anyway, so there has never been a plaintext-only MariaDB here. What `tls.enabled` selects is whether the instance gets a *managed* TLS setup — its own CA and server certificate issued through cert-manager — rather than the operator's own CA. It is managed automatically when `external` is `true`, and can be turned on independently by setting `tls.enabled: true`.

Enforcement is separate, and opt-in. `tls.required: true` sets `require_secure_transport=ON`, after which plaintext connections are refused. It defaults to `false`, so enabling managed TLS does not by itself cut off any existing client. It applies whether or not TLS is managed — enforcement works against the operator's own certificates too — so setting it is never silently ignored.

The default is deliberate: enforcement has to follow the trust anchor rather than lead it. Until the platform publishes the CA to the tenant automatically, requiring TLS would tell clients they must encrypt while giving them nothing to verify the server against. Turn it on once your clients have the CA — see below.

> This default is temporary. `tls.required` will default to `true` in a later release, as an announced change, once the published trust anchor is available everywhere.

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

Turning on managed TLS does not disconnect anything by itself: enforcement is off by default, so existing clients keep working while you migrate them. What does change is who issues the server certificate — the CA moves from the operator's to one issued for this instance. A client that verifies against `<instance>-ca-bundle` follows that automatically, because the bundle carries the old and the new CA together; a client that copied `ca.crt` somewhere and pinned it has to re-read it.

The order that avoids downtime:

1. Enable managed TLS and leave enforcement off (the default):

   ```yaml
   tls:
     enabled: true
   ```

2. Distribute `<instance>-ca-bundle` and move clients onto verified TLS one at a time.
3. Once no plaintext clients remain, require it:

   ```yaml
   tls:
     enabled: true
     required: true
   ```

Step 3 is the only one that can refuse a connection, and it happens when you choose it rather than when a platform upgrade rolls through.

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

| Name           | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Type     | Value  |
| -------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------ |
| `tls`          | TLS configuration. The chart always asks the operator to serve TLS, so these settings select CA ownership and enforcement rather than whether TLS exists. Managed automatically when `external` is true.                                                                                                                                                                                                                                                                                                                      | `object` | `{}`   |
| `tls.enabled`  | Manage TLS for this instance: issue a dedicated CA and server certificate through cert-manager. This does not switch TLS on, and does not enforce it: the chart always asks the operator to serve TLS, so what this selects is whether the instance gets its own CA instead of the operator's. Enforcement is separate and opt-in, see `required`. When omitted, defaults to the value of `external`.                                                                                                                         | `*bool`  | `null` |
| `tls.required` | Enforce TLS for all connections (sets MariaDB require_secure_transport=ON). Applies whether or not TLS is managed. Enforcement is opt-in and defaults to false: it has to follow the trust anchor, not lead it, because requiring TLS before the platform publishes a CA to the tenant would demand encryption while leaving clients nothing to verify the server against. Set to true once your clients have the CA. This default is temporary and flips to true as an announced change, together with the published anchor. | `*bool`  | `null` |


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
