# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## Parameters

### Common parameters

| Name               | Description                                                                                                                     | Type       | Value     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------- |
| `replicas`         | Number of Redis replicas.                                                                                                       | `int`      | `2`       |
| `resources`        | Explicit CPU and memory configuration for each Redis replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`      |
| `resources.cpu`    | CPU available to each replica.                                                                                                  | `quantity` | `""`      |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                         | `quantity` | `""`      |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                         | `string`   | `t1.nano` |
| `size`             | Persistent Volume Claim size available for application data.                                                                    | `quantity` | `1Gi`     |
| `storageClass`     | StorageClass used to store the data.                                                                                            | `string`   | `""`      |
| `external`         | Enable external access from outside the cluster.                                                                                | `bool`     | `false`   |
| `version`          | Redis major version to deploy                                                                                                   | `string`   | `v8`      |


### TLS parameters

| Name              | Description                                                                                                                                                                                                                                                                                                                         | Type     | Value  |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------ |
| `tls`             | TLS configuration. TLS is opt-in and is not inferred from `external`.                                                                                                                                                                                                                                                               | `object` | `{}`   |
| `tls.enabled`     | Enable TLS for Redis and Sentinel connections. Disabled unless set. Enabling it moves Redis and Sentinel to a TLS-only listener, so existing plaintext clients must be migrated at the same time. Encryption is provided by the redis-operator fork that mounts the certificate Secret into both Redis and Sentinel pods at `/tls`. | `*bool`  | `null` |
| `tls.authClients` | Maps to the Redis `tls-auth-clients` directive. Defaults to `no` — the server certificate is presented but client certificates are not validated.                                                                                                                                                                                 | `string` | `{}`   |


### Application-specific parameters

| Name          | Description                 | Type   | Value  |
| ------------- | --------------------------- | ------ | ------ |
| `authEnabled` | Enable password generation. | `bool` | `true` |


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

`tls.enabled` turns on TLS for Redis and Sentinel connections. It is off unless set, including when `external` is true.

> **Enabling TLS is a one-way door for existing clients.** TLS replaces plaintext rather than running beside it: with TLS on, Redis and Sentinel are configured with `port 0` and serve only on the TLS port. Every client of that instance has to move to TLS at the same time, using the CA certificate described below. This is why TLS is not inferred from `external` — an instance reachable from outside the cluster is the one most likely to have clients that an upgrade must not disconnect.

Enabling TLS makes the chart issue a per-release cert-manager chain: a self-signed bootstrap Issuer, a CA certificate, a CA Issuer, and the server leaf certificate the operator mounts into the Redis and Sentinel pods. The CA belongs to this release alone; it is not a cluster-wide trust root, and nothing outside the release trusts it.

To verify the server, a client needs that CA certificate. The operator publishes it as the Secret `<release>-ca-cert`, which holds only `ca.crt` and no private key, and the release grants tenant read access to it:

```sh
kubectl get secret redis-<name>-ca-cert -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
redis-cli --tls --cacert ca.crt -h <host> -p 6379
```

`<host>` has to be a name the certificate covers. In-cluster that is any of the `rfr-`, `rfrm-`, `rfrs-` and `rfs-` service names, and those resolve normally.

From outside the cluster the only covered name is `<release>.<tenant-host>`, and the chart does not publish DNS for it: the external Service is a plain LoadBalancer with no `external-dns` annotation, so nothing points that name at the LoadBalancer address. Connecting to the LoadBalancer IP instead fails hostname verification, because the only IP addresses in the certificate are the loopback ones the in-pod probes and the metrics sidecar use. Until the name is published, an external client has to be pointed at it manually — a DNS record or a hosts entry mapping `<release>.<tenant-host>` to the LoadBalancer address.

Neither the CA private key (`<release>-ca-tls`) nor the server leaf and its private key (`<release>-tls`) is readable by the tenant. The first would allow minting certificates that any client trusting this release accepts; the second would allow impersonating this release's Redis endpoints.

Certificate renewal needs a restart, and only the Redis half is automated. The leaf certificate is valid for a year and cert-manager renews it 30 days before expiry, but Redis and Sentinel read `tls-cert-file` and `tls-key-file` once at startup and never re-read them. When TLS is on, the release annotates the RedisFailover with `secret.reloader.stakater.com/reload`, which the operator copies onto the Redis StatefulSet, so the platform's reloader rolls the Redis pods when the Secret changes.

**Sentinel is not covered.** The same annotation cannot reach it: the operator builds the Sentinel Deployment with no `metadata.annotations` field at all, and `spec.sentinel.podAnnotations` lands on the pod template, which is not where reloader looks. So after a renewal the Sentinel pods keep serving the previous certificate until something restarts them, and roughly 30 days later that certificate expires. Restart the Sentinel pods after a renewal, or on a schedule shorter than the `renewBefore` window, until this is fixed upstream.

Closing the gap needs a change in the operator, not the chart: the Sentinel Deployment has to carry the RedisFailover's own annotations the way the Redis StatefulSet already does, at which point the existing annotation covers both. Note the failure is partial rather than total in the meantime — the CA uses `rotationPolicy: Never`, so a rolled Redis and a stale Sentinel still chain to the same CA and keep verifying each other.

`tls.authClients` maps to the Redis `tls-auth-clients` directive and defaults to `no`, meaning the server presents its certificate and does not ask connecting clients for one. Setting it to `optional` or `yes` makes Redis request, and for `yes` require, a client certificate signed by the same per-release CA.

Note that the platform does not currently mint a client certificate for the tenant, and the CA private key needed to sign one is deliberately out of reach. Redis and Sentinel pods and the metrics sidecar authenticate with the leaf certificate the operator mounts for them, so they are unaffected, but an external client has no supported way to obtain a certificate this CA will accept. Use `authClients: yes` only when the client certificate is supplied out of band by whoever also controls the CA.
