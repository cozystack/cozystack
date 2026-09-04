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

> Kubernetes objects for an instance are named after the Helm release, which prefixes the instance name: an instance `mydb` produces the Service and Secrets `mariadb-mydb-*`. The names below use `mariadb-<name>` for that reason, while `<instance>` elsewhere in this document is the name of the `MariaDB` resource itself.

TLS is always served. `tls.issuer` selects who issues the certificate:

| `tls.issuer` | issued by | covers |
| --- | --- | --- |
| `operator` (default) | mariadb-operator's own CA | `mariadb-<name>`, `-primary`, `-secondary`, the headless service, `localhost` |
| `cert-manager` | a CA issued for this instance | the same, plus the external hostname when `external` is `true` |

The operator has no way to learn the external hostname, so verifying a connection from outside the cluster requires `tls.issuer: cert-manager`.

The default is `operator`, and it is not derived from `external`: switching issuer re-issues the server certificate under a new authority, which breaks any client that pinned the previous `ca.crt` without making anything safer — those instances already serve TLS.

> The field is named for the issuer, not for on/off, because TLS is on either way. Some other charts here spell a similar-looking `tls.enabled` that *does* switch TLS on and off; this one deliberately does not, and the enum keeps it from reading as a switch. Epic [#2811](https://github.com/cozystack/cozystack/issues/2811) tracks converging the TLS surface across charts.

Enforcement is separate. `tls.required: true` sets `require_secure_transport=ON` and refuses plaintext, under either issuer. It defaults to `false` because turning it on disconnects every client not yet using TLS — a decision to make once clients have the CA, not something an upgrade does.

> `tls.required` will default to `true` in a later release, as an announced change.

Connect with the MariaDB client by supplying the CA and asking for full verification:

```bash
mysql -h mariadb-<name> -u <user> -p --ssl-ca=/path/to/ca.crt --ssl-verify-server-cert
```

`-p` without a value prompts for the password, which keeps it out of shell history and out of the process arguments other users can read.

`--ssl-ca` supplies the trust anchor; `--ssl-verify-server-cert` is what checks the server name against the certificate. Whether that second flag defaults to on depends on the version of the *client* you connect with, not the server version selected by `version`: it is off through 10.11 and on from 11.4. Do not read that as "an 11.4 client already verifies". Without `--ssl-ca` it has no trust anchor to verify against, and falls back to an anti-tampering check derived from the password hash, which establishes neither the chain nor the hostname — and cannot be derived at all for a passwordless user. `--ssl-ca` is the load-bearing flag here; pass both and the connection is verified on either client. Note these are MariaDB client options — the MySQL 8 client spells the same intent as `--ssl-mode=VERIFY_IDENTITY`, which the MariaDB client rejects outright.

The trust anchor is published to tenants as `mariadb-<name>.tenant-ca`, an object holding `ca.crt` and nothing else, delivered through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant. It is lifted from the operator's own key-free `mariadb-<name>-ca-bundle`, which is reconciled whenever TLS is enabled and follows CA rollovers, but that Secret is not itself granted to tenants: a direct grant on the name would convey whatever happens to occupy it, where the projection conveys only what the platform has vouched for.

```bash
kubectl get tenantsecret mariadb-<name>.tenant-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
```

> **Verifying an external connection requires `tls.issuer: cert-manager`.** The operator's certificate does not carry the external hostname, so a client connecting from outside with `--ssl-verify-server-cert` fails hostname verification against an unmanaged instance no matter which CA it trusts. A cert-manager-issued certificate is what puts `mariadb-<name>.<host>` in the certificate.
>
> Such clients also need a DNS record pointing at the LoadBalancer address: the hostname is a SAN, the address is not, so connecting to the IP directly fails verification either way.

#### Migrating an existing instance

An existing instance keeps the operator's CA and its current enforcement whether or not you upgrade; setting `tls.issuer: cert-manager` is what moves it to a CA issued for it, and that is the step to plan around.

The deprecated `backup.*` CronJob also changes: its client now verifies the server it connects to, against `mariadb-<name>-ca-bundle`, where before it connected without checking. That bundle is present on every instance and carries whichever CA is in use — the operator's, and the chart's as well under `issuer: cert-manager` — and the certificate covers the hostname the job dials, so the verification itself needs no action. Note that this job is separately unable to start — it reads the root password from a Secret name the chart does not render — so on current releases the change is to a job that does not run. Use the `BackupClass` / `Plan` flow instead.

Switching issuer does not enforce anything by itself — enforcement is a separate opt-in — but it does change who issues the server certificate. A client that re-reads `mariadb-<name>.tenant-ca` at connect time follows the change automatically, because the operator keeps both the old and the new CA in `mariadb-<name>-ca-bundle`, which the projection stays in sync with. A client that copied `ca.crt` out and pinned it will fail chain validation once the new certificate is served, and has to re-read the anchor.

> On a fresh instance created with `tls.issuer: cert-manager`, the pods do not start until cert-manager has issued the certificates: the operator needs the server certificate before it creates the StatefulSet. This resolves on its own within a reconcile or two — the chart labels the Secrets so the operator is woken as soon as they appear — but the instance looks stalled in the meantime.

The order to change it in:

1. Switch the issuer and leave enforcement off (the default):

   ```yaml
   tls:
     issuer: cert-manager
   ```

   This step rolls the StatefulSet. The CR gains `serverCertSecretRef` and `serverCASecretRef`, which changes the name of the Secret projected into the pods, and a pod-template change restarts them. On a multi-replica instance that is a rolling restart; on a single-replica one it is downtime for as long as one pod takes to come back. Wait for `kubectl rollout status statefulset/mariadb-<name>` before going on.

2. Distribute `mariadb-<name>.tenant-ca` and move clients onto verified TLS one at a time.
3. Once no plaintext clients remain, require it:

   ```yaml
   tls:
     issuer: cert-manager
     required: true
   ```

Step 3 is the only one that refuses a client by policy, and it happens when you choose it rather than when a platform upgrade rolls through. Step 1 interrupts connections as well, but by restarting the server rather than by rejecting anyone: a client that reconnects is served.

Going back is the same change in reverse: setting `tls.issuer: operator` hands issuance back, and clients have to pick up the operator's CA from the bundle again.

One thing does not clean itself up. The Certificates are removed, but cert-manager is configured not to own the Secrets it issued, so `mariadb-<name>.ca-tls` (the CA private key) and `mariadb-<name>.tls` (the server private key) stay in the namespace. No tenant grant exposes them, but they are private keys, so remove them.

Wait for the rollout first. `mariadb-<name>.tls` is mounted into the running pods for as long as the instance still serves the chart-issued certificate, so deleting it before the operator has reissued and rolled the StatefulSet leaves the next pod unable to start:

```bash
kubectl rollout status statefulset/mariadb-<name>
kubectl delete secret mariadb-<name>.ca-tls mariadb-<name>.tls --ignore-not-found
```

Uninstalling the instance does this for you; only the turn-it-off-and-keep-running case needs the manual step.

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

| Name           | Description                                                                                                                                                                                                                                                                                                                                 | Type     | Value      |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ---------- |
| `tls`          | TLS configuration. Selects who issues the certificates and whether plaintext is refused; TLS itself is always served.                                                                                                                                                                                                                       | `object` | `{}`       |
| `tls.issuer`   | Who issues the server certificate: `operator` uses the CA mariadb-operator manages itself, `cert-manager` gives the instance its own CA and is what covers the external hostname. Named for the issuer because TLS is served under both, unlike the similarly-spelled `tls.enabled` in some other charts, which does switch TLS on and off. | `string` | `operator` |
| `tls.required` | Refuse plaintext connections (sets MariaDB require_secure_transport=ON). Applies under either issuer. Opt-in: turn it on once every client connects over TLS, since it disconnects those that do not.                                                                                                                                       | `*bool`  | `null`     |


### Version parameters

| Name      | Description                           | Type     | Value   |
| --------- | ------------------------------------- | -------- | ------- |
| `version` | MariaDB major.minor version to deploy | `string` | `v11.8` |


### Application-specific parameters

| Name                             | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                | Type                | Value |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------- | ----- |
| `users`                          | Users configuration map. Passwords (including the `root` account) are always auto-generated and stored in the `<release>-credentials` Secret; they cannot be set from values. Read a user's password from that Secret, or rotate every managed password by bumping `passwordRotation`. A `password` left over in values from before the field was removed is ignored by the chart but stays the live credential until a `passwordRotation` bump retires it.                                                                                                                                                                                                                                                                                | `map[string]object` | `{}`  |
| `users[name].maxUserConnections` | Maximum number of connections.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | `int`               | `0`   |
| `passwordRotation`               | Rotation counter for auto-generated application-user passwords. Change it to any new value (0 -> 1 -> 2 ...; any change, not only an increment, triggers a rotation) to regenerate every managed user password on the next reconcile; leaving it unchanged keeps the passwords already stored in the `<release>-credentials` Secret. Existing releases adopt their current passwords as the baseline on first upgrade: the counter value is recorded without rotating, so a bump requested during that same first upgrade does not rotate — bump it once more afterwards to rotate a legacy release. The `root` password is auto-generated but not rotated by this counter, since mariadb-operator only applies it at datadir bootstrap. | `int`               | `0`   |
| `databases`                      | Databases configuration map.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | `map[string]object` | `{}`  |
| `databases[name].roles`          | Roles assigned to users.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `object`            | `{}`  |
| `databases[name].roles.admin`    | List of users with admin privileges.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       | `[]string`          | `[]`  |
| `databases[name].roles.readonly` | List of users with read-only privileges.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | `[]string`          | `[]`  |


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
  user2:
    maxUserConnections: 1000
```

Passwords cannot be set here — they (and the `root` account) are auto-generated and stored in the `<release>-credentials` Secret. Read a user's password with `kubectl get secret <release>-credentials -o jsonpath='{.data.user1}' | base64 -d`, and rotate every managed application-user password by bumping `passwordRotation`.


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
