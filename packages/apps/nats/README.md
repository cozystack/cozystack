# Managed NATS Service

NATS is an open-source, simple, secure, and high performance messaging system.
It provides a data layer for cloud native applications, IoT messaging, and microservices architectures.

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                    | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------ | ---------- | --------- |
| `replicas`         | Number of replicas.                                                                                                            | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each NATS replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                 | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                        | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                        | `string`   | `t1.nano` |
| `storageClass`     | StorageClass used to store the data.                                                                                           | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                               | `bool`     | `false`   |
| `tls`              | TLS configuration. When omitted, TLS follows the `external` flag.                                                              | `object`   | `{}`      |
| `tls.enabled`      | Enable TLS. When omitted, TLS is enabled automatically when `external` is true.                                                | `*bool`    | `null`    |


### Application-specific parameters

| Name                   | Description                                                   | Type                | Value  |
| ---------------------- | ------------------------------------------------------------- | ------------------- | ------ |
| `users`                | Users configuration map.                                      | `map[string]object` | `{}`   |
| `users[name].password` | Password for the user.                                        | `string`            | `""`   |
| `jetstream`            | Jetstream configuration.                                      | `object`            | `{}`   |
| `jetstream.enabled`    | Enable or disable Jetstream for persistent messaging in NATS. | `bool`              | `true` |
| `jetstream.size`       | Jetstream persistent storage size.                            | `quantity`          | `10Gi` |
| `config`               | NATS configuration.                                           | `object`            | `{}`   |
| `config.merge`         | Additional configuration to merge into NATS config.           | `*object`           | `{}`   |
| `config.resolver`      | Additional resolver configuration to merge into NATS config.  | `*object`           | `{}`   |


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

### tls

`tls.enabled` is tri-state. Left unset it follows `external`, so TLS is enabled automatically for a NATS instance exposed outside the cluster and disabled for one that is not. Setting it explicitly overrides that in either direction.

When TLS is on, the chart issues a self-signed CA and a server certificate from it, and NATS serves the client port on `:4222` with the server certificate.

**Retrieving the CA certificate** for client verification:

The trust anchor is published as `<release>.tenant-ca`: an object holding `ca.crt` and nothing else, delivered to tenants through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant. The projection is created by the platform's CA-extraction controller, so it is present only on a platform build that ships that controller.

```bash
kubectl --context <ctx> --namespace <tenant> \
  get tenantsecret nats-<name>.tenant-ca \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode
```

`<release>.tenant-ca` is the only object that hands over the CA certificate without also handing over a private key, which is why it exists. The chart's own `nats-<name>-ca` Secret stores the CA **private key** under `tls.key` alongside the certificate — read access to it would let the holder issue certificates for anything, so it is never granted to a tenant. The same applies to `nats-<name>-tls`, which holds the server's private key.

It is reached through `tenantsecrets` rather than by reading the Secret directly, and that is deliberate: `tenantsecrets` surfaces only objects the platform has vouched for, whereas a direct grant on the name would convey whatever happens to occupy that name.
