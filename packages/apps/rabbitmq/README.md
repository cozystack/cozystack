# Managed RabbitMQ Service

RabbitMQ is a robust message broker that plays a crucial role in modern distributed systems. Our Managed RabbitMQ Service simplifies the deployment and management of RabbitMQ clusters, ensuring reliability and scalability for your messaging needs.

## Deployment Details

The service utilizes official RabbitMQ operator. This ensures the reliability and seamless operation of your RabbitMQ instances.

- Github: https://github.com/rabbitmq/cluster-operator/
- Docs: https://www.rabbitmq.com/kubernetes/operator/operator-overview.html

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                        | Type       | Value     |
| ------------------ | ---------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of RabbitMQ replicas.                                                                                                       | `int`      | `3`       |
| `resources`        | Explicit CPU and memory configuration for each RabbitMQ replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                     | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                            | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                            | `string`   | `t1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                       | `quantity` | `10Gi`    |
| `storageClass`     | StorageClass used to store the data.                                                                                               | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                   | `bool`     | `false`   |
| `tls`              | TLS configuration. When `tls.enabled` is not set, TLS is enabled automatically when `external` is true.                            | `object`   | `{}`      |
| `tls.enabled`      | Enable TLS for AMQPS (5671) and Management HTTPS (15671). When omitted, defaults to the value of `external`.                       | `*bool`    | `null`    |
| `version`          | RabbitMQ major.minor version to deploy                                                                                             | `string`   | `v4.2`    |


### Application-specific parameters

| Name                          | Description                      | Type                | Value |
| ----------------------------- | -------------------------------- | ------------------- | ----- |
| `users`                       | Users configuration map.         | `map[string]object` | `{}`  |
| `users[name].password`        | Password for the user.           | `string`            | `""`  |
| `vhosts`                      | Virtual hosts configuration map. | `map[string]object` | `{}`  |
| `vhosts[name].roles`          | Virtual host roles list.         | `object`            | `{}`  |
| `vhosts[name].roles.admin`    | List of admin users.             | `[]string`          | `[]`  |
| `vhosts[name].roles.readonly` | List of readonly users.          | `[]string`          | `[]`  |


## TLS scope

This chart adds TLS listeners for AMQP (port 5671) and the management HTTPS interface (port 15671) using cert-manager. It adds encrypted endpoints; it does not close the unencrypted ones.

**The plaintext listeners stay open.** AMQP on 5672 and management on 15672 keep listening whenever TLS is on, and when `external` is `true` they are published on the LoadBalancer alongside the TLS ports. The operator's `spec.tls.disableNonTLSListeners` is the only switch that closes them, and it closes them at the broker level — it writes `listeners.tcp = none` into `rabbitmq.conf`, so the socket is never opened on any interface and in-cluster clients lose plaintext access at the same instant as external ones. There is no operator-supported way to drop a port from the published Service alone: `spec.override.service.spec.ports` is applied as a strategic merge patch keyed on `port`, which can add or mutate entries but never remove them, and the operator regenerates its default port set on every reconcile. This chart therefore does not set the flag, and connecting over TLS is currently the client's choice rather than something the endpoint enforces. Treat an `external: true` RabbitMQ as reachable in plaintext from outside the cluster and restrict it at the network layer if that is not acceptable.

Intra-cluster Erlang distribution (port 25672) also remains plaintext, and `disableNonTLSListeners` would not change that either — the operator never applies it to the headless Service that carries the distribution port. Inter-node mTLS via `spec.tls.caSecretName` is not wired by this chart.

## Verifying the server certificate

The certificates are issued by a per-release self-signed CA, so a client must be given that CA to verify the endpoint. The trust anchor is published as `<release>.tenant-ca`: an object holding `ca.crt` and nothing else, delivered through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant.

```bash
kubectl get tenantsecret <release>.tenant-ca \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
```

`<release>.tenant-ca` is the only object that hands over the CA certificate without also handing over a private key, which is why it exists. The cert-manager Secrets are deliberately not granted to tenants: `<release>-ca` stores the CA **private key**, so read access would let the holder issue certificates for anything, and the leaf `<release>-tls` stores the server private key next to the same `ca.crt`. RBAC has no key-level filter, so granting either to deliver a trust anchor would deliver a key with it.

> **Requires the CA-extraction controller.** This chart labels its CA for publication, but the platform component that performs the projection ships separately. Until it is present, `<release>.tenant-ca` is not created and the command above returns `NotFound`; the TLS configuration itself is unaffected. On a cluster without it, a tenant has no key-free path to the CA.

> **Warning:** the CA is issued with a 5-year duration and cert-manager renews the certificate as it approaches expiry, so a bundle copied once will eventually stop verifying. Re-read `<release>.tenant-ca` on a schedule, or mount it and let the kubelet refresh it, instead of baking `ca.crt` into a client image or a truststore built at release time.

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
| `nano`      | `100m` | `128Mi` |
| `micro`     | `250m` | `256Mi` |
| `small`     | `500m` | `512Mi` |
| `medium`    | `500m` | `1Gi`   |
| `large`     | `1`    | `2Gi`   |
| `xlarge`    | `2`    | `4Gi`   |
| `2xlarge`   | `4`    | `8Gi`   |

