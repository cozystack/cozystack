# Managed Redis Service

Redis is a highly versatile and blazing-fast in-memory data store and cache that can significantly boost the performance of your applications. Managed Redis Service offers a hassle-free solution for deploying and managing Redis clusters, ensuring that your data is always available and responsive.

## Deployment Details

Service utilizes the Spotahome Redis Operator for efficient management and orchestration of Redis clusters. 

- Docs: https://redis.io/docs/
- GitHub: https://github.com/spotahome/redis-operator

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


### Application-specific parameters

| Name                              | Description                                                                                                                                                                                                                | Type       | Value                             |
| --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | --------------------------------- |
| `authEnabled`                     | Enable password generation.                                                                                                                                                                                                | `bool`     | `true`                            |
| `tls`                             | TLS configuration. When enabled, an nginx stream sidecar terminates TLS on port 6380. Certificate chain is managed by the cozy-tls sub-chart.                                                                              | `object`   | `{}`                              |
| `tls.enabled`                     | Enable TLS termination via nginx stream sidecar and cert-manager PKI chain.                                                                                                                                                | `bool`     | `false`                           |
| `tls.clusterDomain`               | Cluster domain used when constructing fully-qualified DNS SANs from dnsNameSuffixes.                                                                                                                                       | `string`   | `cozy.local`                      |
| `tls.issuerRef`                   | External Issuer reference. When name is empty, a self-signed CA chain is created.                                                                                                                                          | `object`   | `{}`                              |
| `tls.issuerRef.name`              | Issuer/ClusterIssuer resource name. When empty, a self-signed CA chain is created.                                                                                                                                         | `string`   | `""`                              |
| `tls.issuerRef.kind`              | Either "Issuer" or "ClusterIssuer".                                                                                                                                                                                        | `string`   | `Issuer`                          |
| `tls.issuerRef.group`             | Issuer API group.                                                                                                                                                                                                          | `string`   | `cert-manager.io`                 |
| `tls.ca`                          | CA certificate settings (only used with self-signed chain).                                                                                                                                                                | `object`   | `{}`                              |
| `tls.ca.duration`                 | CA certificate lifetime.                                                                                                                                                                                                   | `string`   | `43800h`                          |
| `tls.certificate`                 | Leaf certificate settings.                                                                                                                                                                                                 | `object`   | `{}`                              |
| `tls.certificate.secretName`      | Custom TLS secret name. Defaults to "<release>-tls".                                                                                                                                                                       | `string`   | `""`                              |
| `tls.certificate.duration`        | Certificate lifetime.                                                                                                                                                                                                      | `string`   | `8760h`                           |
| `tls.certificate.renewBefore`     | Renew this long before expiry.                                                                                                                                                                                             | `string`   | `720h`                            |
| `tls.certificate.dnsNames`        | Explicit DNS SANs for the certificate. Merged with computed names from dnsNameSuffixes. When external is true and tls is enabled, add the LoadBalancer IP or hostname here so external clients can verify the certificate. | `[]string` | `[]`                              |
| `tls.certificate.dnsNameSuffixes` | Service name suffixes used to auto-compute DNS SANs. Each suffix expands to three DNS forms using the release name and namespace.                                                                                          | `[]string` | `[master, replicas, external-lb]` |
| `tls.certificate.usages`          | Key usages.                                                                                                                                                                                                                | `[]string` | `[server auth]`                   |
| `tls.certificate.encoding`        | Private key encoding.                                                                                                                                                                                                      | `string`   | `PKCS8`                           |


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
