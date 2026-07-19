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

### TLS Mode

TLS is always enabled and fully managed by the PSMDB operator via its native cert-manager integration. The operator creates the complete certificate chain:

- A self-signed CA Issuer and CA Certificate
- A CA-backed Issuer
- A leaf TLS certificate with SANs covering the replica set service, per-pod DNS names, wildcards, and localhost

The chart does **not** render cert-manager `Issuer` or `Certificate` resources. The operator manages these directly, and chart-rendered cert-manager objects would be orphaned and ignored as trust anchors.

TLS mode is set to `preferTLS`, which means the MongoDB server accepts both TLS and non-TLS connections. This is also the operator's own default, so the chart states it explicitly rather than changing behaviour; `spec.secrets.ssl` is likewise set explicitly to the name the operator would default to.

> **Note:** `preferTLS` accepts plaintext connections, so it authenticates the server to clients that opt in but does not enforce TLS. Moving to `requireTLS` is a breaking change for any existing plaintext client and is out of scope here. To enforce TLS at the network layer in the meantime, restrict client access via NetworkPolicy.
>
> The operator also defaults `tls.allowInvalidCertificates` to `true`, and this chart leaves that default in place, so the server does not reject clients presenting an invalid or mismatched certificate. Combined with `preferTLS`, the guarantee this configuration provides is that a client which opts into TLS can verify the *server*; it is not mutual authentication and it does not keep unverified clients out. Both are inherited operator defaults rather than choices made here, and tightening either is a separate breaking change.

> **External hostname SANs:** When `external: true` is enabled, the chart advertises per-pod LoadBalancer hostnames to the operator via `splitHorizons`, and the operator folds those hostnames into the leaf certificate SAN list. External clients can then verify the TLS hostname, provided `<release>-rs0-<i>.<host>` resolves to the corresponding per-pod LoadBalancer — DNS is an operational prerequisite you must satisfy.
>
> That fold is gated on two versions, and this chart controls only one of them. It sets `crVersion: 1.22.0` itself, but the PSMDB operator must also be at 1.22.0 or newer, and the operator is a separate platform component (`packages/system/mongodb-operator`, currently 1.22.0) with its own lifecycle. On a platform carrying an older operator the gate is not met and nothing reports it: the chart still renders `splitHorizons`, the CR is still accepted, the cluster still comes up healthy, and the external hostnames simply never enter the SAN list. A client that then enables hostname verification fails the handshake. If external verification does not work, check the operator version before suspecting DNS.
>
> The external hostnames are derived from `_namespace.host`, which the Cozystack controller injects per tenant. Whenever it is empty, TLS stays enabled but `splitHorizons` is omitted, so the certificate does not cover external hostnames and a client connecting to the LoadBalancer with hostname verification on will fail the handshake. Nothing goes red when this happens — the render succeeds and the cluster comes up healthy.
>
> That empty state has two causes and only one of them heals. During the brief window before the controller populates the field (or when the chart is rendered outside Cozystack), it resolves on the next reconcile. But a tenant with no host of its own and no ancestor supplying one resolves to an empty host permanently — `packages/apps/tenant` computes it from `.Values.host` or the parent's, and leaves it empty when neither is set. On such a tenant `external: true` works for IP-based access and external TLS hostname verification is simply unavailable; set a host on the tenant (or an ancestor) if you need it. The chart does not fail the render in this case, because that would break existing hostless tenants already running `external: true`.

### Retrieving the CA bundle

> **Requires the CA-extraction controller.** This chart declares where its trust anchor comes from, but the platform component that publishes `<release>.tenant-ca` ships separately. Until it is present the projection is not created and the command below returns `NotFound`; the TLS configuration itself is unaffected. On a cluster without it, a tenant has no key-free path to the CA — the operator's `<release>-ca-cert` is deliberately not granted, because it carries the CA private key.
>
> **Requires cert-manager.** The declared source is the `<release>-ca-cert` Secret, which the operator creates only on its cert-manager path. Without cert-manager the operator falls back to issuing certificates itself, producing no `<release>-ca-cert` at all and embedding `ca.crt` inside the leaf Secrets instead — so the declared source resolves to nothing and no trust anchor is published. Cozystack ships cert-manager as a platform component, so this affects only clusters where it has been removed.

The trust anchor is published as `<release>.tenant-ca`: an object holding `ca.crt` and nothing else, delivered to tenants through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant.

```bash
kubectl --context <ctx> --namespace <tenant> \
  get tenantsecret <release>.tenant-ca \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode
```

`<release>.tenant-ca` is the only object that hands over the CA certificate without also handing over a private key, which is why it exists. The PSMDB operator creates its own `<release>-ca-cert` Secret, but that one stores the CA **private key** alongside the certificate — read access would let the holder issue certificates for anything, so it is never granted to a tenant. The same applies to the leaf `<release>-ssl`, which holds the server private key.

It is reached through `tenantsecrets` rather than by reading the Secret directly, and that is deliberate: `tenantsecrets` surfaces only objects the platform has vouched for, whereas a direct grant on the name would convey whatever happens to occupy that name.

> **Name clash worth knowing.** The shared `cozy-lib` TLS helper documents `<release>-ca-cert` as the conventional name for a **key-free** trust anchor. For MongoDB that exact name is already taken by the operator's **key-bearing** CA Secret. The same string therefore sits on opposite sides of the boundary depending on the engine — which is why this chart declares its source by name and publishes under the distinct `<release>.tenant-ca` instead of adopting the convention.

> **Warning:** The CA rotates. The operator issues it with a 365-day duration and cert-manager renews it roughly 30 days before expiry, so a bundle copied once will stop verifying within a year. Re-read `<release>.tenant-ca` on a schedule (or mount it and let the kubelet refresh it) instead of baking `ca.crt` into a client image, ConfigMap, or truststore built at release time.

### Verifying a connection

Write the trust anchor to a file, then read the connection string out of `<release>-credentials`:

```bash
kubectl --context <ctx> --namespace <tenant> \
  get tenantsecret <release>.tenant-ca \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode > ca.crt

kubectl --context <ctx> --namespace <tenant> \
  get secret <release>-credentials \
  --output jsonpath='{.data.uri}' | base64 --decode
```

`mongosh` takes the bundle as a file, and the two TLS inputs are passed as flags rather than folded into the URI:

```bash
mongosh "<uri>" --tls --tlsCAFile ./ca.crt
```

Drivers take the same two inputs under their own names (`tls=true` plus a CA path). Prefer the flag form over appending `tls=true` to the connection string: the replica-set URI already carries a `?replicaSet=rs0` query and the sharded one carries none, so the correct separator differs between the two topologies.

> **Omitting the TLS flags does not fail.** Because the server runs in `preferTLS`, a client that leaves them out is accepted on a plaintext connection and nothing reports the downgrade — the session simply is not encrypted. Verification is opt-in on the client side, so treat a working connection as evidence of nothing until the flags are present.

Hostname verification succeeds against the in-cluster names out of the box, since the operator covers them in the leaf SANs. Verifying against an external LoadBalancer endpoint depends on the `splitHorizons` fold described above, and is unavailable in sharded mode — see below.

### Sharding and TLS

In-cluster `mongos` SANs are fully covered: the operator appends the `mongos` and config-replset names (short, namespace, FQDN, and wildcard forms) to every certificate, for sharded and non-sharded clusters alike.

> **Warning:** External TLS hostname verification is not available when `sharding: true`. In sharded mode external access goes through the single `<release>-external` LoadBalancer fronting `mongos`, and no SAN covers that name — `splitHorizons` is replica-set scoped and is only rendered for the non-sharded topology. Clients connecting to a sharded cluster from outside must either verify against an in-cluster `mongos` name that resolves to the same endpoint, or connect without hostname verification. Combining `sharding: true` with `external: true` and full verification is unsupported.

### External Access

When `external: true` is enabled:
- **Replica Set mode**: The operator exposes each replica set member through its own LoadBalancer via `replsets[].expose` and rewrites the replica set configuration so drivers can discover the primary from outside the cluster. There is no single load-balanced endpoint: the driver connects to the members and routes writes to the primary itself, using the replica set connection string.
- **Sharded mode**: Traffic is routed through mongos routers, which handle both reads and writes correctly.

### Credentials

On first install, the credentials secret will be empty until the Percona operator initializes the cluster.
Run `helm upgrade` after MongoDB is ready to populate the credentials secret with the actual password.

### Data lifecycle

When the MongoDB release is uninstalled, the operator finalizers reclaim release-scoped resources:

**Reclaimed by the `percona.com/delete-psmdb-pvc` finalizer:**

- All PVCs backing the replica set storage. Whether the underlying PersistentVolume and on-disk data are actually deleted depends on the StorageClass `reclaimPolicy` (`Delete` removes them, `Retain` leaves them for manual cleanup).
- Operator-managed secrets:
  - `<release>-percona-server-mongodb-users` — operator users credentials
  - `internal-<release>` — internal operator state
  - `internal-<release>-users` — operator-internal users data
  - `<release>-mongodb-encryption-key` — at-rest encryption key

**Reclaimed by `helm uninstall`:**

- `<release>-credentials` — connection string for application code
- `<release>-user-<username>` — per-user passwords
- `<release>-s3-creds` — backup destination credentials (if backups are configured)

**Reclaimed by ownerReference garbage collection:**

- TLS secrets `<release>-ssl`, `<release>-ssl-internal`, and `<release>-ca-cert`, along with the cert-manager `Issuer` and `Certificate` objects the operator creates. The cascade works out either way on the `--enable-certificate-owner-ref` fork: with the flag the Certificate owns the Secret and the CR owns the Certificate; without it (the default Cozystack ships) the operator makes the CR the direct owner of the Secret.

  > **The Secret ownerReference is applied at reconcile time, not at creation.** cert-manager creates these Secrets unowned, and the operator patches the CR reference on afterwards, in `WaitForCerts`. Two paths skip the patch: a dry-run reconcile returns before it, and a Secret already present under the same name whose `cert-manager.io/certificate-name` annotation does not match is counted toward success and returned on without being patched. So a Secret that predates the release at one of these names — from a restore-from-backup, a rename, or a manual recreate — can stay unowned and survive uninstall. The finalizer does not cover the gap: it enumerates only the users, `internal-<release>`, `internal-<release>-users`, and encryption-key Secrets, and no TLS Secret is on that list. After deleting a release, confirm `<release>-ca-cert` is gone rather than assuming it — it holds the CA private key, and an orphan of it is the one leak this chart's TLS posture exists to prevent.
- The `<release>.tenant-ca` projection, where the CA-extraction controller is present to publish it, since it is owner-referenced to the release's HelmRelease.

**Not reclaimed automatically:**

- `<release>-ssl-old` and `<release>-ssl-internal-old`, only if a rotation was interrupted. These are operator scratch state, not residue: the operator snapshots the previous leaf certificates under these names when it rotates the cert-manager chain, and deletes them itself in the same pass once the merged CA has landed in the live secrets. They are created with no ownerReference, so a rotation interrupted between the snapshot and the merge can leave them behind, and those stragglers do survive uninstall.

  > **Do not delete these while a rotation is in flight.** The operator reads the previous CA back out of exactly these two names to build the merged trust bundle that keeps pods on old and new certificates talking to each other during the rolling restart. Removing them mid-rotation destroys the only copy of the old CA. Sweep them only if they are still present long after the cluster has settled, which indicates an interrupted rotation rather than one in progress.

**Recovery from a stuck deletion:**

If the `psmdb-operator` is uninstalled before MongoDB CRs are deleted, the finalizers cannot run and the `PerconaServerMongoDB` CR hangs in `Terminating`. To recover, clear the finalizers manually:

```bash
kubectl --namespace <namespace> patch psmdb <release> --type merge --patch '{"metadata":{"finalizers":[]}}'
```

Note that this skips the operator-driven cleanup — PVCs and operator-managed secrets will remain orphaned and must be removed manually.

If you need to retain data, take a backup before deletion. Refer to the [Percona Operator for MongoDB documentation](https://docs.percona.com/percona-operator-for-mongodb/) for backup/restore workflows.

### Upgrading from earlier versions

Earlier versions of this chart referenced a namespace-shared system users secret (`percona-server-mongodb-users`). Upgrading to a release that scopes this secret per CR (`<release>-percona-server-mongodb-users`) triggers a password rotation for the operator-managed system users. The rotation is performed in place by the Percona operator via `db.changeUserPassword()` against the running mongod (operator log: `Secret data changed. Updating users...`); pods are not restarted and the cluster stays available.

**Rotated automatically on upgrade:**

- The five operator-managed system accounts: `databaseAdmin`, `userAdmin`, `backup`, `clusterAdmin`, `clusterMonitor`.
- Secret `<release>-percona-server-mongodb-users` (newly created, per-CR) and `internal-<release>-users` receive the new values.
- Secret `<release>-credentials` is regenerated; its `password` and `uri` keys reflect the new `databaseAdmin` password.

**Not affected:**

- Custom users defined under `users:` in chart values. Their `<release>-user-<name>` secrets are not touched.
- The at-rest encryption key (`<release>-mongodb-encryption-key`) and replica set keyfile (`<release>-mongodb-keyfile`) are unchanged, so on-disk data remains readable.

**Action required after upgrade:**

Workloads that mount `<release>-credentials` keep using the cached old password until they re-read the secret. Restart those pods, or run a controller such as [Reloader](https://github.com/stakater/Reloader) to roll them automatically. Without this, application connections fail with authentication errors once their existing sessions expire.

**Orphaned legacy secret:**

The previous namespace-shared secret `percona-server-mongodb-users` is no longer referenced by any MongoDB CR after upgrade, but the operator does not garbage-collect it. If multiple MongoDB releases in the same namespace previously shared it, all of them rotate to their own per-CR secrets — passwords are no longer shared across CRs in the namespace, which is the intended outcome. Confirm no other consumers reference it, then remove it manually:

```bash
kubectl --namespace <namespace> delete secret percona-server-mongodb-users
```

**Rolling restart on upgrade, for external replica set releases only:**

A release already running with `external: true` in replica set mode gains `splitHorizons` on upgrade. The per-pod external hostnames become certificate SANs, so cert-manager reissues `<release>-ssl`; the operator hashes that Secret into a pod-template annotation, which rolls the replica set StatefulSet. The restart is ordered and the replica set stays available through it, but it is a restart of the database and worth scheduling.

The same pass snapshots the superseded certificates as `<release>-ssl-old` and `<release>-ssl-internal-old`, then deletes them once the merged CA has landed. Because this upgrade changes only the SAN list and not the CA itself, the merge is a no-op and both snapshots are removed on the first pass — no cleanup is expected. See the lifecycle section above for the interrupted case, and do not delete them by hand while the rollout is running.

Scaling an external replica set down can also produce transient reconfiguration errors during the upgrade. MongoDB requires horizons on all members or none, and a terminating pod is still a member while the chart advertises horizons only for the remaining ones. The operator retries and the condition clears once the pod is gone.

Releases with `external: false`, and sharded releases in any configuration, are unaffected: neither renders `splitHorizons`, so no SAN changes and nothing restarts.

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                       | Type       | Value      |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `replicas`         | Number of MongoDB replicas in replica set.                                                                                        | `int`      | `3`        |
| `resources`        | Explicit CPU and memory configuration for each MongoDB replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                    | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                           | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                           | `string`   | `t1.small` |
| `size`             | Persistent Volume Claim size available for application data.                                                                      | `quantity` | `10Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                              | `string`   | `""`       |
| `external`         | Enable external access from outside the cluster.                                                                                  | `bool`     | `false`    |
| `version`          | MongoDB major version to deploy.                                                                                                  | `string`   | `v8`       |


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

| Name                   | Description                                        | Type                | Value |
| ---------------------- | -------------------------------------------------- | ------------------- | ----- |
| `users`                | Users configuration map.                           | `map[string]object` | `{}`  |
| `users[name].password` | Password for the user (auto-generated if omitted). | `string`            | `""`  |


### Databases configuration

| Name                             | Description                                                | Type                | Value |
| -------------------------------- | ---------------------------------------------------------- | ------------------- | ----- |
| `databases`                      | Databases configuration map.                               | `map[string]object` | `{}`  |
| `databases[name].roles`          | Roles assigned to users.                                   | `object`            | `{}`  |
| `databases[name].roles.admin`    | List of users with admin privileges (readWrite + dbAdmin). | `[]string`          | `[]`  |
| `databases[name].roles.readonly` | List of users with read-only privileges.                   | `[]string`          | `[]`  |


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

