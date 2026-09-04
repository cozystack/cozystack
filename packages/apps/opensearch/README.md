# Managed OpenSearch Service

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## TLS and client verification

Chart-managed HTTP TLS is on by default for a release published outside the cluster (`external: true`) and off for a cluster-internal one; `tls.enabled` overrides it either way. It requires OpenSearch 2.0.0 or later: below that an explicit `tls.enabled: true` fails the release, and an auto-on one falls back to operator-managed HTTP TLS so an existing release keeps working. When it is on, the chart issues the HTTPS certificate for the OpenSearch API and, when Dashboards is enabled, a separate one for Dashboards, both from a private CA minted per release. Transport mTLS between nodes stays operator-managed either way.

**Retrieving the CA bundle** for client verification:

The trust anchor is published as `opensearch-<name>.tenant-ca`, where `<name>` is the name of the OpenSearch resource: an object holding `ca.crt` and nothing else, created for every TLS-enabled release and delivered to tenants through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant.

```bash
kubectl --context <ctx> --namespace <tenant> \
  get tenantsecret opensearch-<name>.tenant-ca \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode
```

That object is the only one that hands over the CA certificate without also handing over a private key, which is why it exists. `opensearch-<name>.http-ca` holds the CA private key alongside the certificate, and each leaf Secret holds a server private key; none of the three is granted to a tenant.

### Upgrading an existing release published externally

A release with `external: true` that never set `tls.enabled` runs operator-managed HTTP TLS today and moves to chart-managed TLS the first time it reconciles after this version. That rolls the cluster and re-anchors it on a new CA, so anything pinned to the operator's CA must be repointed at `opensearch-<name>.tenant-ca`. Set `tls.enabled: false` before upgrading to stay as you are.

The published external-dns name changes with the same upgrade: the LoadBalancer Services move from the in-cluster domain, which never resolved outside the cluster, to `<release>.<tenant-host>` and `<release>-dashboards.<tenant-host>`. external-dns is upsert-only and never issues a delete, so the stale record keeps pointing at the LoadBalancer until it is removed from the zone by hand.

## Parameters

### Common parameters

| Name                   | Description                                                                                                                       | Type       | Value       |
| ---------------------- | --------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----------- |
| `replicas`             | Number of OpenSearch nodes in the cluster.                                                                                        | `int`      | `3`         |
| `resources`            | Explicit CPU and memory configuration for each OpenSearch node. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`        |
| `resources.cpu`        | CPU available to each node.                                                                                                       | `quantity` | `""`        |
| `resources.memory`     | Memory (RAM) available to each node.                                                                                              | `quantity` | `""`        |
| `resourcesPreset`      | Default sizing preset used when `resources` is omitted. OpenSearch requires minimum 2Gi memory.                                   | `string`   | `c1.medium` |
| `size`                 | Persistent Volume Claim size available for application data.                                                                      | `quantity` | `10Gi`      |
| `storageClass`         | StorageClass used to store the data.                                                                                              | `string`   | `""`        |
| `external`             | Enable external access from outside the cluster.                                                                                  | `bool`     | `false`     |
| `topologySpreadPolicy` | How strictly to enforce pod distribution across nodes and zones.                                                                  | `string`   | `soft`      |
| `version`              | OpenSearch major version to deploy.                                                                                               | `string`   | `v2`        |


### TLS configuration

| Name          | Description                                                                                                                                     | Type     | Value  |
| ------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------ |
| `tls`         | HTTP-layer TLS configuration.                                                                                                                   | `object` | `{}`   |
| `tls.enabled` | Tri-state TLS switch. When unset, TLS is enabled automatically if external is true, off otherwise. Set explicitly to true or false to override. | `*bool`  | `null` |


### Image configuration

| Name                | Description                            | Type     | Value |
| ------------------- | -------------------------------------- | -------- | ----- |
| `images`            | Container images used by the operator. | `object` | `{}`  |
| `images.opensearch` | OpenSearch image.                      | `string` | `""`  |


### Node roles configuration

| Name               | Description                   | Type     | Value   |
| ------------------ | ----------------------------- | -------- | ------- |
| `nodeRoles`        | Node roles configuration.     | `object` | `{}`    |
| `nodeRoles.master` | Enable cluster_manager role.  | `bool`   | `true`  |
| `nodeRoles.data`   | Enable data role.             | `bool`   | `true`  |
| `nodeRoles.ingest` | Enable ingest role.           | `bool`   | `true`  |
| `nodeRoles.ml`     | Enable machine learning role. | `bool`   | `false` |


### Users configuration

| Name                   | Description                                        | Type                | Value |
| ---------------------- | -------------------------------------------------- | ------------------- | ----- |
| `users`                | Custom OpenSearch users configuration map.         | `map[string]object` | `{}`  |
| `users[name].password` | Password for the user (auto-generated if omitted). | `string`            | `""`  |
| `users[name].roles`    | List of OpenSearch roles.                          | `[]string`          | `[]`  |


### OpenSearch Dashboards configuration

| Name                          | Description                                                                                                                                                                | Type       | Value      |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `dashboards`                  | OpenSearch Dashboards configuration.                                                                                                                                       | `object`   | `{}`       |
| `dashboards.enabled`          | Enable OpenSearch Dashboards deployment. At a 42-character application name the Dashboards Service is not created: its name would exceed the 63-character DNS label limit. | `bool`     | `false`    |
| `dashboards.replicas`         | Number of Dashboards replicas.                                                                                                                                             | `int`      | `1`        |
| `dashboards.resources`        | Explicit CPU and memory configuration for Dashboards.                                                                                                                      | `object`   | `{}`       |
| `dashboards.resources.cpu`    | CPU available to each node.                                                                                                                                                | `quantity` | `""`       |
| `dashboards.resources.memory` | Memory (RAM) available to each node.                                                                                                                                       | `quantity` | `""`       |
| `dashboards.resourcesPreset`  | Default sizing preset for Dashboards.                                                                                                                                      | `string`   | `c1.small` |

