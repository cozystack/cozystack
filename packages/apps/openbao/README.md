# Managed OpenBAO Service

OpenBAO is an open-source secrets management solution forked from HashiCorp Vault.
It provides identity-based secrets and encryption management for cloud infrastructure.

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Auto-unseal

By default OpenBAO uses Shamir key shares: after every pod restart an operator has to unseal each replica by hand. Setting `seal.type: static` switches the instance to OpenBAO's [`seal "static"`](https://openbao.org/docs/configuration/seal/static/), which unseals the barrier from a 32-byte key read from a Secret you manage. The chart never generates, stores or rotates the key; it only mounts the Secret you name.

1. Create the key Secret in the release namespace. The value is the base64 text of 32 random bytes, stored under the key `key`:

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

   `keyId` is a permanent identifier for that key material. OpenBAO refuses to start with a key whose identifier it has already seen with different material, so change `keyId` whenever you change the key.

To rotate the key:

1. Create a second Secret with the new key material and pick a new `keyId`.
2. Move the current pair to `previousSecretName`/`previousKeyId`, put the new pair in `secretName`/`keyId` and upgrade the release. This only changes the StatefulSet template. The upstream chart uses the `OnDelete` update strategy and the server copies its HCL to `/tmp/storageconfig.hcl` at startup, so running pods keep the old key and the old config until they are replaced.
3. Replace the pods. Standalone: delete the single pod. HA: delete the standby pods one at a time, waiting for each to rejoin and unseal, then delete the active pod so that leadership moves to a pod that already runs the new config. Do not delete the active pod first: once a node with the new config has taken over, a standby still on the old template cannot unseal after a restart.

   ```bash
   kubectl -n <namespace> delete pod <release>-2   # standby
   kubectl -n <namespace> delete pod <release>-1   # standby
   kubectl -n <namespace> delete pod <release>-0   # active, last
   ```

4. Confirm the rotation before touching the old key. The node that becomes active with the new config re-wraps the stored barrier and recovery keys with `current_key` and logs it:

   ```bash
   kubectl -n <namespace> logs <active pod> | grep -E 'upgrading (stored keys|recovery key)'
   ```

   Until those lines have appeared, storage is still wrapped with the previous key. The static seal decrypts by the key identifier stored with the data and refuses an identifier it does not know (`unknown key id for data`), which is why both keys have to stay configured until the re-wrap is done.
5. Drop the `previous*` values, upgrade, and replace the pods once more in the same order. A pod that unseals with the new key alone proves the re-wrap. Delete the old Secret last.

> Switching an existing Shamir-sealed instance to `static` is a seal migration and needs `bao operator unseal -migrate`; see the OpenBAO [seal migration](https://openbao.org/docs/concepts/seal/#seal-migration) documentation. The chart does not run the migration for you. It does refuse to change `seal.type` on an instance that already runs with another seal, because a plain config change would leave a barrier nobody can decrypt. Set `seal.allowMigration: true` only for the upgrade in which you run the migration, then remove it. Migrating away from `static` needs the old stanza kept with `disabled = "true"` and its key still mounted, which this chart does not render; that direction is refused regardless of `allowMigration`.

Keep a copy of the key outside the cluster: a Raft snapshot or a data PVC is unreadable without it.

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

| Name                      | Description                                                                                                                                                                                                 | Type     | Value    |
| ------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | -------- |
| `seal`                    | Seal configuration. Defaults to Shamir key shares; set `type: static` for auto-unseal.                                                                                                                      | `object` | `{}`     |
| `seal.type`               | Seal type. `shamir` keeps the current behaviour; `static` enables auto-unseal from `secretName`.                                                                                                            | `string` | `shamir` |
| `seal.secretName`         | Existing Secret in the release namespace whose key `key` holds the base64 text of 32 random bytes (for example `openssl rand -base64 32`). Required when `type` is `static`.                                | `string` | `""`     |
| `seal.keyId`              | Permanent identifier of the current key. Change it whenever the key material changes; OpenBAO refuses a key whose identifier it has already seen with different material. Required when `type` is `static`. | `string` | `""`     |
| `seal.previousSecretName` | Secret holding the previous key during an n-1 key rotation. Keep it until the pods have been replaced and the active node has logged `upgrading stored keys`; see the README.                               | `string` | `""`     |
| `seal.previousKeyId`      | Identifier of the previous key. Required when `previousSecretName` is set.                                                                                                                                  | `string` | `""`     |
| `seal.allowMigration`     | Acknowledge a seal migration. The chart refuses to change `type` on an existing instance unless this is `true`; set it only while running `bao operator unseal -migrate`, then remove it.                   | `bool`   | `false`  |

