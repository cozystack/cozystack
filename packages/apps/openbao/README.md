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

To rotate the key, create a second Secret, move the current pair to `previousSecretName`/`previousKeyId` and put the new pair in `secretName`/`keyId`. This is the n-1 rotation the static seal documents: `current_key` is used for new seal operations while `previous_key` still decrypts what was written before. Keep both Secrets in place until OpenBAO has re-wrapped storage with the new key, then drop the `previous*` values and delete the old Secret.

> Switching an existing Shamir-sealed instance to `static` (or back) is a seal migration and needs `bao operator unseal -migrate`; see the OpenBAO [seal migration](https://openbao.org/docs/concepts/seal/#seal-migration) documentation. The chart does not run the migration for you.

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
| `seal.secretName`         | Existing Secret in the release namespace whose key `key` holds the base64 text of 32 random bytes (`head -c 32 /dev/urandom | base64 | tr -d '\n'`). Required when `type` is `static`.                      | `string` | `""`     |
| `seal.keyId`              | Permanent identifier of the current key. Change it whenever the key material changes; OpenBAO refuses a key whose identifier it has already seen with different material. Required when `type` is `static`. | `string` | `""`     |
| `seal.previousSecretName` | Secret holding the previous key during an n-1 key rotation.                                                                                                                                                 | `string` | `""`     |
| `seal.previousKeyId`      | Identifier of the previous key. Required when `previousSecretName` is set.                                                                                                                                  | `string` | `""`     |

