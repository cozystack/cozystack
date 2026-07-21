# Managed RabbitMQ Service

RabbitMQ is a robust message broker that plays a crucial role in modern distributed systems. Our Managed RabbitMQ Service simplifies the deployment and management of RabbitMQ clusters, ensuring reliability and scalability for your messaging needs.

## Deployment Details

The service utilizes official RabbitMQ operator. This ensures the reliability and seamless operation of your RabbitMQ instances.

- Github: https://github.com/rabbitmq/cluster-operator/
- Docs: https://www.rabbitmq.com/kubernetes/operator/operator-overview.html

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name                         | Description                                                                                                                                                                                                                                                                                          | Type       | Value     |
| ---------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`                   | Number of RabbitMQ replicas.                                                                                                                                                                                                                                                                         | `int`      | `3`       |
| `resources`                  | Explicit CPU and memory configuration for each RabbitMQ replica. When omitted, the preset defined in `resourcesPreset` is applied.                                                                                                                                                                   | `object`   | `{}`      |
| `resources.cpu`              | CPU available to each replica.                                                                                                                                                                                                                                                                       | `quantity` | `""`      |
| `resources.memory`           | Memory (RAM) available to each replica.                                                                                                                                                                                                                                                              | `quantity` | `""`      |
| `resourcesPreset`            | Default sizing preset used when `resources` is omitted.                                                                                                                                                                                                                                              | `string`   | `t1.nano` |
| `size`                       | Persistent Volume Claim size available for application data.                                                                                                                                                                                                                                         | `quantity` | `10Gi`    |
| `storageClass`               | StorageClass used to store the data.                                                                                                                                                                                                                                                                 | `string`   | `""`      |
| `external`                   | Enable external access from outside the cluster.                                                                                                                                                                                                                                                     | `bool`     | `false`   |
| `tls`                        | TLS configuration. TLS is off unless `tls.enabled` is set to true.                                                                                                                                                                                                                                   | `object`   | `{}`      |
| `tls.enabled`                | Enable TLS for AMQPS (5671) and Management HTTPS (15671). Applies whether or not `external` is set. Defaults to false.                                                                                                                                                                               | `bool`     | `false`   |
| `tls.disableNonTLSListeners` | Close the plaintext AMQP (5672) and management (15672) listeners. This is a broker-level switch: it drops in-cluster clients as well as external ones, and it cannot be combined with `users` or `vhosts`. Erlang distribution (25672) and epmd (4369) stay plaintext regardless. Defaults to false. | `bool`     | `false`   |
| `version`                    | RabbitMQ major.minor version to deploy                                                                                                                                                                                                                                                               | `string`   | `v4.2`    |


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

This chart can add TLS listeners for AMQP (port 5671) and the management HTTPS interface (port 15671) using cert-manager. Enabling it adds encrypted endpoints without closing the unencrypted ones — but it does change which endpoint clients are pointed at, and `tls.disableNonTLSListeners` can close the plaintext ones outright. Both are covered below.

**TLS is off unless you ask for it.** `tls.enabled` defaults to `false` and nothing else turns it on — in particular `external` does not. Publishing a broker outside the cluster and encrypting it are separate decisions, so an existing instance keeps its listeners and its published connection details across an upgrade of this chart, and every consequence listed below follows an explicit edit rather than arriving on its own.

**Enabling TLS does not close plaintext, inside or outside the cluster.** AMQP on 5672 and management on 15672 keep listening whenever TLS is on, and when `external` is `true` they are published on the public LoadBalancer alongside the TLS ports. Enabling TLS adds encrypted endpoints; using them is the client's choice rather than something the endpoint enforces. Treat an `external: true` RabbitMQ as reachable in plaintext from outside the cluster — with TLS on or off — and restrict it at the network layer if that is not acceptable.

**Plaintext cannot be closed on the public side alone.** There is no operator-supported way to drop a port from the published Service by itself: `spec.override.service.spec.ports` is applied as a strategic merge patch keyed on `port`, so it can add or mutate entries but never remove them, and the operator regenerates its default port set on every reconcile. The only switch this chart exposes is `tls.disableNonTLSListeners`, described below, and it closes plaintext everywhere at once rather than only on the public side — in-cluster clients included.

A narrower variant — closing AMQP at the broker while leaving the operator's own management path on plaintext, which would keep chart-managed `users` and `vhosts` working — looks reachable through `spec.rabbitmq.additionalConfig`, but this chart does not wire it and it has not been validated here. Treat it as an open question rather than a supported option.

**Setting `tls.enabled: true` on a running instance changes three things.** Plan for them before flipping it.

The broker pods roll once, because the operator mounts the new TLS Secret into the pod template and that is a StatefulSet template change. The instance gains a dependency on cert-manager, since the operator will not finish reconciling until the Secret has been issued.

And **the published metrics port changes**. The operator exposes *either* `prometheus` (15692) *or* `prometheus-tls` (15691) on its Service, never both — deliberately, so that a scrape config selecting every port does not count each node twice — and that choice keys off TLS alone. Enabling TLS therefore withdraws 15692 from the Service and publishes 15691 in its place. The broker itself keeps listening on 15692, but a `ServiceMonitor` or `VMServiceScrape` selecting the port by the name `prometheus` stops matching. Retarget it to `prometheus-tls`, which is HTTPS under the release CA and so needs a `tlsConfig` carrying the trust anchor described below. The chart does not paper over this by re-adding 15692 through `spec.override.service`: the operator drops any port name it does not own on each reconcile and the override would reinstate it without a NodePort, which on a LoadBalancer means a fresh allocation and another reconcile every round.

And **the connection details handed to clients change**. `<release>-default-user` is the Service Binding object this chart grants to the tenant, and the operator rewrites its `port` key from `5672` to `5671` as soon as `spec.tls.secretName` is set — gated on TLS alone, not on whether the plaintext listener is still open. A client that hardcodes `:5672` keeps working, because that listener does stay open. A client that resolves its endpoint from the binding — the documented way to consume it — follows the change to 5671 and must speak TLS from then on, and it cannot verify the certificate until the trust anchor below is obtainable. Per-plugin port keys (`mqtt-port`, `stomp-port`, `stream-port` and the web variants) move to their TLS values the same way. This is the main reason `tls.enabled` is opt-in rather than something `external` implies: it moves clients that read the binding correctly, and it moves them before there is a key-free way to hand them the CA.

**The plaintext management listener is load-bearing.** `messaging-topology-operator` is what reconciles the `User`, `Vhost` and `Permission` objects this chart creates from `users` and `vhosts`, and it talks to the broker over the management API. Once TLS is on it would switch to HTTPS on 15671 and verify the broker against the operator process's system certificate pool, which cannot contain a per-release self-signed CA — so every user and permission would fail to reconcile with `certificate signed by unknown authority`. The chart therefore annotates the cluster with `rabbitmq.com/operator-connection-uri` pointing at plaintext `http://…:15672`, keeping that operator on the in-cluster plaintext port. This affects only the operator's own management connection, never broker traffic, and it is why `tls.disableNonTLSListeners` cannot be combined with `users` or `vhosts`.

**Erlang distribution and epmd stay plaintext no matter what you set.** Inter-node distribution (25672) and epmd (4369) are carried on the headless Service, which `disableNonTLSListeners` never touches — the flag does not apply to them. So `tls.disableNonTLSListeners: true` does not mean "TLS everywhere": it means every *client-facing* listener is TLS-only while inter-node traffic stays in the clear. Inter-node mTLS via `spec.tls.caSecretName` is not wired by this chart.

### Closing the plaintext listeners

`tls.disableNonTLSListeners` is opt-in and defaults to `false`, so an upgrade never closes a listener a client is already using. Setting it to `true` writes `listeners.tcp = none` into `rabbitmq.conf`, a broker-level directive: the socket is not opened on any interface, so **in-cluster clients lose plaintext at the same instant external ones do**. There is no setting that closes it externally while keeping it internally. Move every client to 5671 and 15671 first, then set the flag.

Two consequences to know before enabling it:

- **It puts the whole cluster out of reach of `messaging-topology-operator`, and the chart refuses to render if `users` or `vhosts` are set.** That operator reconciles those objects over the management API, and the flag forces its connection onto TLS regardless of how it is configured — computed as `TLSEnabled() && (!connectUsingHTTP || DisableNonTLSListeners())`. It then verifies the broker against the operator process's system trust pool, which cannot contain a per-release CA, so every `User`, `Vhost` and `Permission` stops reconciling. Pointing it back at plaintext is no escape, because that is the port the flag closes. Note the scope: that calculation reads the `RabbitmqCluster` object, not the origin of the topology CR, so moving users and vhosts out of `values.yaml` into hand-written `User`/`Vhost`/`Permission` objects does **not** work around it — they fail the same way, just without the chart's refusal to warn you. Once this flag is on, manage users and permissions through `rabbitmqctl` or a definitions import instead.
- The plaintext metrics port closes with it, leaving only `prometheus-tls` on 15691.

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

