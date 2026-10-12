# Managed OpenSearch Service

> `storageClass` is annotated as immutable in the chart schema — see [`docs/storage-immutability.md`](../../../docs/storage-immutability.md) for the contract and which consumers enforce it.

## TLS and client verification

The HTTP listener and Dashboards always serve TLS. `tls.issuer` selects who signs the certificate: `cert-manager` mints a private CA per release, `operator` leaves it to the CA the opster operator manages itself. Left unset, the field follows `external`, so a release published outside the cluster gets `cert-manager` and a cluster-internal one gets `operator`. Only `cert-manager` requires OpenSearch 2.0.0 or later: below that, choosing it explicitly fails the release, and a release that only set `external` falls back to the operator issuer so it keeps working. Under `cert-manager` the chart issues the certificate for the OpenSearch API and, when Dashboards is enabled, a separate one for Dashboards, both from that per-release CA. Transport mTLS between nodes stays operator-managed under either issuer.

**Retrieving the CA bundle** for client verification:

The trust anchor is published as `opensearch-<name>.tenant-ca`, where `<name>` is the name of the OpenSearch resource: an object holding `ca.crt` and nothing else, created for every release on the `cert-manager` issuer and delivered to tenants through the `core.cozystack.io/tenantsecrets` API that the base tenant roles already grant.

```bash
kubectl --context <ctx> --namespace <tenant> \
  get tenantsecret opensearch-<name>.tenant-ca \
  --output jsonpath='{.data.ca\.crt}' | base64 --decode
```

That object is the only one that hands over the CA certificate without also handing over a private key, which is why it exists. `opensearch-<name>.http-ca` holds the CA private key alongside the certificate, and each leaf Secret holds a server private key; none of the three is granted to a tenant.

### Operating a release on the cert-manager issuer

Neither OpenSearch nor Dashboards reloads its certificate while running, so a reissue reaches no pod on its own and nothing anywhere reports that one happened. That is not only about a deliberate rotation: changing `external`, the tenant host or the cluster domain changes the certificate's SANs, cert-manager reissues on the spot, and the pods keep serving the old material with no error, event or condition to show for it. All three inputs reissue both certificates, so complete such a change on both sides.

```bash
kubectl --context <ctx> --namespace <tenant> delete secret opensearch-<name>-http-server
kubectl --context <ctx> --namespace <tenant> rollout restart statefulset opensearch-<name>-nodes

# only when Dashboards is enabled
kubectl --context <ctx> --namespace <tenant> delete secret opensearch-<name>-dashboards-server
kubectl --context <ctx> --namespace <tenant> rollout restart deployment opensearch-<name>-dashboards
```

Do not delete `opensearch-<name>.http-ca` to force this. That mints a new CA, and until everything has restarted the old one is still what clients are told to trust.

Moving back to the operator issuer leaves the key material behind. The platform runs cert-manager with `enableCertificateOwnerRef: false`, so removing a `Certificate` does not remove the `Secret` it wrote: `opensearch-<name>.http-ca` keeps a usable CA private key at a predictable name for as long as the namespace lives, and each leaf Secret keeps a server key. No tenant Role and no selector reaches any of them, so this is hygiene rather than exposure, but nothing collects them either.

```bash
kubectl --context <ctx> --namespace <tenant> delete secret \
  opensearch-<name>.http-ca opensearch-<name>-http-server

# only if Dashboards was enabled
kubectl --context <ctx> --namespace <tenant> delete secret opensearch-<name>-dashboards-server
```

The trust anchor needs no such step. The projection carries an owner reference to the sentinel the chart removes with the issuer, so it is collected along with it and no tenant is left trusting a CA the endpoint has stopped presenting.

### Upgrading a release whose application name is too long for Dashboards

With `dashboards.enabled` the application name is limited to 41 characters, and to 32 when `external` is also on, because the operator builds the Dashboards Service as `opensearch-<name>-dashboards` and a longer name overflows the 63-character DNS label limit. The apps API admits 42, so a release can exist today at a length this version refuses, and on upgrade its HelmRelease stops rendering with that length in the message.

Such a release is not being broken by the guard. The Service it needs was already being refused by the API server, and the operator returns that failure on every pass before it reaches its upgrade, restart and snapshot-repository reconcilers, so the release has not been upgrading or rolling since it was created. What changes is that the stall now announces itself.

The application name is fixed and cannot be renamed in place, so the way out for an existing release is `dashboards.enabled: false`, which drops the bound and lets it converge again without a Dashboards deployment it had no working Service for. Keeping Dashboards means recreating the application under a shorter name.

### Upgrading an existing release published externally

A release with `external: true` that never set `tls.issuer` runs on the operator issuer today and moves to `cert-manager` the first time it reconciles after this version. That rolls the cluster and re-anchors it on a new CA, so anything pinned to the operator's CA must be repointed at `opensearch-<name>.tenant-ca`. Set `tls.issuer: operator` before upgrading to keep that half as it is.

The published external-dns name changes with the same upgrade whatever `tls.issuer` says, because the Services are gated on `external` alone: they move from the in-cluster domain, which never resolved outside the cluster, to `<release>.<tenant-host>` and `<release>-dashboards.<tenant-host>`. external-dns is configured upsert-only here, so it issues no delete and the stale record keeps pointing at the LoadBalancer until it is removed from the zone by hand.

Nothing lists the records that are left behind, and the cleanup is per zone rather than per release, so start from the releases that publish one.

```bash
kubectl --context <ctx> get opensearches.apps.cozystack.io --all-namespaces \
  --output=custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,EXTERNAL:.spec.external
```

Every release listed with `EXTERNAL` true was annotated for `opensearch-<name>.<namespace>.<cluster-domain>` before the upgrade, and for `opensearch-<name>-dashboards.<namespace>.<cluster-domain>` as well when Dashboards was enabled. Whether an annotation ever became a record depends on the deployment, because external-dns ships with no provider and an empty `domainFilters` and a hosted zone for an in-cluster domain is unusual. Where one was served, those are the names to remove from it; the ones under the tenant host replace them.

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

| Name         | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                             | Type     | Value |
| ------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `tls`        | HTTP-layer TLS configuration. Selects who issues the HTTP server certificate; TLS itself is always served.                                                                                                                                                                                                                                                                                                                                                                              | `object` | `{}`  |
| `tls.issuer` | Who issues the HTTP server certificate: `operator` uses the CA the opster operator manages itself, `cert-manager` gives the release its own CA, covers the external hostname and publishes a trust anchor the tenant can verify against. Unset follows external: `cert-manager` when it is true, `operator` when it is false. Named for the issuer because TLS is served under both, unlike the similarly-spelled `tls.enabled` in some other charts, which does switch TLS on and off. | `string` | `{}`  |


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

| Name                          | Description                                                                                                                                                                                                                                                                                                                                                 | Type       | Value      |
| ----------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ---------- |
| `dashboards`                  | OpenSearch Dashboards configuration.                                                                                                                                                                                                                                                                                                                        | `object`   | `{}`       |
| `dashboards.enabled`          | Enable OpenSearch Dashboards deployment. The application name is then limited to 41 characters, and to 32 when external access is also on, because the Dashboards Service the operator creates would otherwise exceed the 63-character DNS label limit, and the operator stops upgrading and restarting the release for as long as that Service is refused. | `bool`     | `false`    |
| `dashboards.replicas`         | Number of Dashboards replicas.                                                                                                                                                                                                                                                                                                                              | `int`      | `1`        |
| `dashboards.resources`        | Explicit CPU and memory configuration for Dashboards.                                                                                                                                                                                                                                                                                                       | `object`   | `{}`       |
| `dashboards.resources.cpu`    | CPU available to each node.                                                                                                                                                                                                                                                                                                                                 | `quantity` | `""`       |
| `dashboards.resources.memory` | Memory (RAM) available to each node.                                                                                                                                                                                                                                                                                                                        | `quantity` | `""`       |
| `dashboards.resourcesPreset`  | Default sizing preset for Dashboards.                                                                                                                                                                                                                                                                                                                       | `string`   | `c1.small` |

