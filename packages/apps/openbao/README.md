# Managed OpenBAO Service

OpenBAO is an open-source secrets management solution forked from HashiCorp Vault.
It provides identity-based secrets and encryption management for cloud infrastructure.

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Auto-unseal

By default OpenBAO uses Shamir key shares, so after every pod restart an operator has to unseal each replica by hand. Setting `seal.type: static` switches the instance to OpenBAO's [`seal "static"`](https://openbao.org/docs/configuration/seal/static/), which unseals the barrier from a 32-byte key read from a Secret in the tenant namespace. The chart never generates, stores or rotates the key. It only mounts the Secret you name.

The choice is a trade-off. With `static` the key sits in a Secret in the same cluster as the data it unwraps, so the barrier is only as strong as access to Secrets in that namespace, to etcd and to backups. Shamir keeps the key material out of the cluster at the price of a manual unseal after every restart. Pick `static` when availability after a restart matters more than that boundary.

1. A cluster administrator creates the key Secret in the tenant namespace. Tenant roles grant no access to Secrets, so tenant users can neither create the key nor read it back. The pods mount the Secret and do not start until it exists, so have it created before you set `seal.type: static`. The value is the base64 text of 32 random bytes, stored under the key `key`.

   ```bash
   kubectl -n tenant-<name> create secret generic openbao-unseal \
     --from-literal=key="$(head -c 32 /dev/urandom | base64 | tr -d '\n')"
   ```

2. Point the instance at it:

   ```yaml
   seal:
     type: static
     secretName: openbao-unseal
     keyId: key-1
   ```

   `keyId` is a permanent identifier for that key material. OpenBAO stores it with the data the key wraps and picks the key by it, so change `keyId` whenever you change the key.

To rotate the key:

1. Have a cluster administrator create a second Secret with the new key material, and pick a new `keyId`.
2. Move the current pair to `previousSecretName`/`previousKeyId`, put the new pair in `secretName`/`keyId` and upgrade the release. The upgrade changes the config and the StatefulSet template, not the running pods. The upstream chart uses the `OnDelete` update strategy and the server copies its HCL to `/tmp/storageconfig.hcl` at startup, so running pods keep the old key and the old config until they are replaced.
3. Replace the pods. In standalone mode delete the single pod. In HA mode delete the standby pods one at a time, waiting for each to rejoin and unseal, then delete the active pod so that leadership moves to a pod that already runs the new config. Do not delete the active pod first. Once a node with the new config has taken over, a standby still on the old template cannot unseal after a restart.

   ```bash
   kubectl -n <namespace> get pods -l openbao-active=true   # the active pod
   kubectl -n <namespace> delete pod <standby pod>          # each standby, one at a time
   kubectl -n <namespace> delete pod <active pod>           # last
   ```

4. Confirm the rotation before touching the old key. The node that becomes active with the new config re-wraps the stored barrier and recovery keys with `current_key` and logs it. Tenant roles do not grant `pods/log`, so a cluster administrator reads that log.

   ```bash
   kubectl -n <namespace> logs <active pod> | grep -E 'upgrading (stored keys|recovery key)'
   ```

   Until those lines have appeared, storage is still wrapped with the previous key. The static seal decrypts by the key identifier stored with the data and refuses an identifier it does not know (`unknown key id for data`), which is why both keys have to stay configured until the re-wrap is done.
5. Drop the `previous*` values, upgrade, and replace the pods once more in the same order. A pod that unseals with the new key alone proves the re-wrap. The cluster administrator deletes the old Secret last.

### Upgrading an existing instance

The chart labels the server pods with `policy.cozystack.io/allow-to-apiserver: "true"`, which the tenant network policy needs before `service_registration "kubernetes"` can reach the API server (#2793). The StatefulSet uses the `OnDelete` strategy, so pods created before the label was added keep running without it after an upgrade. Delete them once, in HA mode one at a time, so that they come back labelled. With the Shamir seal every replaced pod has to be unsealed again. An HA instance that hangs at start because of #2793 stays hung until its pods are replaced.

### Migrating an existing instance from Shamir

Switching a running Shamir-sealed instance to `static` is a seal migration, see the OpenBAO [seal migration](https://openbao.org/docs/concepts/seal/#seal-migration) documentation. The chart does not run it for you. It refuses a changed `seal.type` on an instance that already runs with another seal, because a plain config change leaves an instance that cannot unseal. OpenBAO documents the migration as a procedure with downtime, so plan a window.

1. Take a restorable backup first, as OpenBAO recommends before any seal migration. In HA mode `bao operator raft snapshot save <file>` writes it. In standalone mode the file storage backend is not transactional, so a copy of the data volume taken while the server runs can be inconsistent. Copy the volume while OpenBAO is stopped, or take a volume snapshot with a storage class that snapshots atomically.
2. A cluster administrator creates the key Secret as described above.
3. Set `seal.type: static` with `secretName` and `keyId`, add `seal.allowMigration: true` and upgrade the release. The StatefulSet uses the `OnDelete` strategy, so the running pods keep the Shamir configuration until they are replaced. This upgrade is the point of no return for the chart. The server config now names the static seal while every pod still runs Shamir, and from here on the chart refuses `seal.type: shamir`. If you abort before any pod is replaced, a cluster administrator has to remove the `seal "static"` stanza from the `openbao-<name>-config` ConfigMap before the release accepts `shamir` again.
4. Standalone. Delete the pod. When it is back, run `bao operator unseal -migrate` with each Shamir unseal key. The server performs the migration and the Shamir keys become the recovery keys.
5. HA. Delete one standby pod, wait for it to come back and run `bao operator unseal -migrate` on it with each Shamir unseal key. Repeat for the other standby pods one at a time. Then run `bao operator step-down` on the active node and wait until another node has become active. The new active node performs the migration and logs its completion. Give that a little time to replicate to the other Raft nodes, then delete the old active pod, so that it restarts with the static configuration and unseals on its own.
6. Remove `seal.allowMigration` and upgrade the release once more.

Migrating away from `static` needs the old stanza kept with `disabled = "true"` and its key still mounted, which this chart does not render. That direction is refused regardless of `allowMigration`.

Keep a copy of the key outside the cluster. A Raft snapshot or a data PVC is unreadable without it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                                                                           | Type       | Value      |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `replicas`         | Number of OpenBAO replicas. HA with Raft is automatically enabled when replicas > 1. Switching between standalone (file storage) and HA (Raft storage) modes requires data migration. | `int`      | `1`        |
| `resources`        | Explicit CPU and memory configuration for each OpenBAO replica. When omitted, the preset defined in `resourcesPreset` is applied.                                                     | `object`   | `{}`       |
| `resources.cpu`    | CPU available to each replica.                                                                                                                                                        | `quantity` | `""`       |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                                                               | `quantity` | `""`       |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                                                               | `string`   | `t1.small` |
| `size`             | Persistent Volume Claim size for data storage.                                                                                                                                        | `quantity` | `10Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                                                                                  | `string`   | `""`       |
| `external`         | Enable external access from outside the cluster.                                                                                                                                      | `bool`     | `false`    |


### Application-specific parameters

| Name | Description                | Type   | Value  |
| ---- | -------------------------- | ------ | ------ |
| `ui` | Enable the OpenBAO web UI. | `bool` | `true` |


### Seal

| Name                      | Description                                                                                                                                                                                                                                                                                                                                                                                                                | Type     | Value    |
| ------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | -------- |
| `seal`                    | Seal configuration. The default is Shamir key shares. Set `type: static` for auto-unseal.                                                                                                                                                                                                                                                                                                                                  | `object` | `{}`     |
| `seal.type`               | Seal type. `shamir` keeps the current behaviour, `static` enables auto-unseal from `secretName`.                                                                                                                                                                                                                                                                                                                           | `string` | `shamir` |
| `seal.secretName`         | Existing Secret in the tenant namespace whose key `key` holds the base64 text of 32 random bytes (for example `openssl rand -base64 32`). A cluster administrator creates it, because tenant roles cannot create Secrets. Lowercase alphanumerics and hyphens, at most 52 characters, because the pod volume is named `userconfig-<name>` and a volume name is a DNS label. Required when `type` is `static`.              | `string` | `""`     |
| `seal.keyId`              | Permanent identifier of the current key. OpenBAO stores it with the data the key wraps and picks the key by it, so change it whenever the key material changes. Lowercase alphanumerics, `-`, `.` and `_`, starting and ending with an alphanumeric, because the upstream chart evaluates the server config as a template and rewrites upper case placeholders such as `HOSTNAME` in it. Required when `type` is `static`. | `string` | `""`     |
| `seal.previousSecretName` | Secret holding the previous key during an n-1 key rotation, with the same name constraints as `secretName`. Keep it until the pods have been replaced and the active node has logged `upgrading stored keys`, see the README.                                                                                                                                                                                              | `string` | `""`     |
| `seal.previousKeyId`      | Identifier of the previous key, with the same character constraints as `keyId`. Required when `previousSecretName` is set.                                                                                                                                                                                                                                                                                                 | `string` | `""`     |
| `seal.allowMigration`     | Acknowledge a seal migration. The chart refuses to change `type` on an existing instance unless this is `true`. Set it only while running `bao operator unseal -migrate`, then remove it.                                                                                                                                                                                                                                  | `bool`   | `false`  |

