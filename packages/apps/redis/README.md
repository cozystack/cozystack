# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the freshworks-oss Redis Operator (a maintained fork of the archived Spotahome operator) for efficient management and orchestration of Redis clusters.

- Docs: https://redis.io/docs/
- GitHub: https://github.com/freshworks-oss/redis-operator

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
| `tls.authClients` | Maps to the Redis `tls-auth-clients` directive. Defaults to `no` — the server certificate is presented but client certificates are not validated. `yes` requires a client certificate signed by this release's CA, which the platform does not issue.                                                                             | `string` | `{}`   |


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

> **TLS is chosen when the instance is created.** TLS replaces plaintext rather than running beside it: with TLS on, Redis and Sentinel are configured with `port 0` and serve only on the TLS port. It cannot be switched on or off for a running instance, because a replica restarted onto the TLS listener cannot replicate from a master still on plaintext, and Sentinels already on TLS would fail over to a stale replica. The operator's RedisFailover schema rejects the change; on an existing instance that surfaces as a failed HelmRelease upgrade while the instance keeps serving what it served before. To add TLS, create a new instance with `tls.enabled` and move the clients and data to it. A `Redis` that was already switched keeps its rejected value until it is put back: the API accepts the change, the HelmRelease then fails on it, and every later edit to that instance waits behind the failure, so remove `tls.enabled` to unblock the release. This is also why TLS is not inferred from `external`: an instance reachable from outside the cluster is the one most likely to have clients that an upgrade must not disconnect.

Enabling TLS makes the chart issue a per-release cert-manager chain: a self-signed bootstrap Issuer, a CA certificate, a CA Issuer, and the server leaf certificate the operator mounts into the Redis and Sentinel pods. The CA belongs to this release alone; it is not a cluster-wide trust root, and nothing outside the release trusts it.

This means TLS requires cert-manager, which the platform installs in the variant-independent part of the `system` bundle, so `isp-hosted` has it too. On a cluster where the cert-manager controller has been removed but its CRDs remain, `tls.enabled` renders `cert-manager.io/v1` resources that nothing issues, and no certificate is ever produced; with the CRDs gone too, the release fails on an unknown kind instead.

To verify the server, a client needs that CA certificate. The operator publishes it as the Secret `<release>.ca-cert`, which holds only `ca.crt` and no private key, and the release grants tenant read access to it:

```sh
kubectl get secret redis-<name>.ca-cert -o jsonpath='{.data.ca\.crt}' | base64 -d > ca.crt
redis-cli --tls --cacert ca.crt -h <host> -p 6379
```

`<host>` has to be a name the certificate covers. In-cluster that is any of the `rfr-`, `rfrm-`, `rfrs-` and `rfs-` service names, and `<release>-external-lb` when `external` is on; all of them resolve normally.

From outside the cluster the only covered name is `<release>.<tenant-host>`, and the chart does not publish DNS for it: the external Service is a plain LoadBalancer with no `external-dns` annotation, so nothing points that name at the LoadBalancer address. Connecting to the LoadBalancer IP instead fails for any client that verifies the hostname, because the only IP addresses in the certificate are the loopback ones the in-pod probes and the metrics sidecar use; `redis-cli` is not such a client, it checks the chain and not the name, so it connects to the IP and hides the mismatch. Until the name is published, an external client has to be pointed at it manually — a DNS record or a hosts entry mapping `<release>.<tenant-host>` to the LoadBalancer address.

Neither the CA private key (`<release>.ca-tls`) nor the server leaf and its private key (`<release>.tls`) is readable by the tenant. The first would allow minting certificates that any client trusting this release accepts; the second would allow impersonating this release's Redis endpoints.

Certificate renewal reaches the pods through the operator. Redis and Sentinel read `tls-cert-file` and `tls-key-file` once at startup and never re-read them, so a renewed Secret changes nothing until the pods restart. On every reconcile the operator reads the TLS Secret and stamps a hash of its `tls.crt` and `ca.crt` into the pod template of both the Redis StatefulSet and the Sentinel Deployment, as the annotation `redis-failover.freshworks.com/tls-secret-hash`. When cert-manager renews the leaf, 30 days before the end of its one-year validity, the hash changes: the operator's own roller replaces the Redis pods one at a time, replicas before the master, and the Deployment controller rolls Sentinel. Nothing needs to touch the release for the renewed certificate to be served.

If the roll cannot happen, because the operator is down or its reconcile of this RedisFailover keeps failing, the deadline is the old certificate's expiry: past it, clients fail to verify, and so do the operator's own TLS connections to Sentinel and Redis, so its checks and healing stop as well. Restart the Redis and Sentinel pods by hand before then.

The CA certificate has its own clock. cert-manager issues it for five years and reissues it on the same key before that ends, so a `ca.crt` copied out of `<release>.tenant-ca` once keeps verifying new leaves only until the original CA certificate expires. Re-read the projection on a schedule shorter than that, or mount it and let the kubelet refresh it, rather than baking the copy into an image.

`tls.authClients` maps to the Redis `tls-auth-clients` directive and defaults to `no`, meaning the server presents its certificate and does not ask connecting clients for one. Setting it to `optional` or `yes` makes Redis request, and for `yes` require, a client certificate signed by the same per-release CA. Quote the value in a manifest: an unquoted `yes` or `no` is read as a boolean and the string field rejects it.

Note that the platform does not currently mint a client certificate for the tenant, and the CA private key needed to sign one is deliberately out of reach. Redis and Sentinel pods and the metrics sidecar authenticate with the leaf certificate the operator mounts for them, so they are unaffected, but an external client has no supported way to obtain a certificate this CA will accept. Use `authClients: yes` only when the client certificate is supplied out of band by whoever also controls the CA.
