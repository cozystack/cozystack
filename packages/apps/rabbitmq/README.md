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

**Upgrading an existing `external: true` instance turns TLS on.** `tls.enabled` defaults to the value of `external`, so an instance that predates this feature gets a certificate chain without any change to its own configuration. Two consequences worth planning for: the broker pods roll once, because the operator mounts the new TLS Secret into the pod template and that is a StatefulSet template change; and the instance gains a dependency on cert-manager, since the operator will not finish reconciling until the Secret has been issued. No client breaks — the plaintext listeners stay open, as above — but a stateful broker does restart. Set `tls.enabled: false` explicitly to keep the previous behaviour.

Intra-cluster Erlang distribution (port 25672) also remains plaintext, and `disableNonTLSListeners` would not change that either — the operator never applies it to the headless Service that carries the distribution port. Inter-node mTLS via `spec.tls.caSecretName` is not wired by this chart.

## Verifying the server certificate

The certificates are issued by a per-release self-signed CA, so a client must be given that CA to verify the endpoint. The trust anchor is published as `<release>.tenant-ca`: an object holding `ca.crt` and nothing else, delivered through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant.

Throughout this section `<release>` means the Helm release name, which is the application name prefixed with `rabbitmq-`. A RabbitMQ named `orders` is release `rabbitmq-orders`, so its trust anchor is `rabbitmq-orders.tenant-ca` and its certificate Secrets are `rabbitmq-orders-tls` and `rabbitmq-orders-ca`.

```bash
kubectl get tenantsecret rabbitmq-<name>.tenant-ca --namespace <tenant-namespace> \
  -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
```

For an external endpoint, note what the certificate does and does not cover. When the platform supplies a root host the certificate carries `rabbitmq-<name>.<host>` as a SAN, but this chart creates no DNS record for that name — `external: true` renders a bare LoadBalancer Service, with no Ingress and no `external-dns` annotation. Verifying by hostname therefore requires pointing that name at the LoadBalancer address yourself. A client connecting straight to the IP will fail hostname verification against the SAN list, since it contains in-cluster names and the unresolved external name only.

`<release>.tenant-ca` is the only object that hands over the CA certificate without also handing over a private key, which is why it exists. The cert-manager Secrets are deliberately not granted to tenants: `<release>-ca` stores the CA **private key**, so read access would let the holder issue certificates for anything, and the leaf `<release>-tls` stores the server private key next to the same `ca.crt`. RBAC has no key-level filter, so granting either to deliver a trust anchor would deliver a key with it.

> **Requires the CA-extraction controller.** This chart labels its CA for publication, but the platform component that performs the projection ships separately. Until it is present, `<release>.tenant-ca` is not created and the command above returns `NotFound`; the TLS configuration itself is unaffected. On a cluster without it, a tenant has no key-free path to the CA.

Verification runs one way only. The server presents a certificate that a client can verify with the CA above; the server does not verify client certificates in return. Enabling that would require `management.ssl.verify = verify_peer` with `fail_if_no_peer_cert` for the management listener and the equivalent `ssl_options.*` for AMQP, none of which this chart sets. Clients continue to authenticate by username and password — carried inside the TLS session rather than in the clear, but still a password rather than a certificate.

### Turning TLS off again

Disabling TLS on a release that had it needs one manual step. Helm removes the `Certificate` objects, but cert-manager does not delete the Secrets they produced (`enableCertificateOwnerRef` is off), so `rabbitmq-<name>-ca` keeps the publication label and the trust anchor keeps being projected for an endpoint that has gone back to plaintext. Nothing the chart renders can prevent that — the label lives on a Secret Helm does not own. Delete `rabbitmq-<name>-ca` and `rabbitmq-<name>-tls` by hand after disabling TLS. Verification fails closed until you do, and no private key is exposed by the stale anchor.

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

