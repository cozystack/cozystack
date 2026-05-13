## Parameters

### Parameters

| Name                          | Description                                                                                                                     | Type       | Value             |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | ---------- | ----------------- |
| `enabled`                     | Enable TLS provisioning for the application.                                                                                    | `bool`     | `false`           |
| `clusterDomain`               | Cluster domain used when constructing default DNS names. Parent charts should forward _cluster["cluster-domain"] to this value. | `string`   | `cozy.local`      |
| `issuerRef`                   | Reference to the cert-manager Issuer or ClusterIssuer that signs certificates.                                                  | `object`   | `{}`              |
| `issuerRef.name`              | Name of the Issuer or ClusterIssuer resource.                                                                                   | `string`   | `""`              |
| `issuerRef.kind`              | Kind of the issuer resource (Issuer or ClusterIssuer).                                                                          | `string`   | `Issuer`          |
| `issuerRef.group`             | API group of the issuer resource.                                                                                               | `string`   | `cert-manager.io` |
| `ca`                          | Configuration for the CA certificate issued by cert-manager.                                                                    | `object`   | `{}`              |
| `ca.duration`                 | Validity duration of the CA certificate.                                                                                        | `string`   | `43800h`          |
| `ca.algorithm`                | Key algorithm for the CA certificate (ECDSA, RSA, or Ed25519).                                                                  | `string`   | `ECDSA`           |
| `ca.size`                     | Key size in bits for the chosen algorithm (not used for Ed25519).                                                               | `int`      | `256`             |
| `certificate`                 | Configuration for the leaf TLS certificate.                                                                                     | `object`   | `{}`              |
| `certificate.secretName`      | Name of the Kubernetes Secret where the certificate is stored.                                                                  | `string`   | `""`              |
| `certificate.duration`        | Validity duration of the leaf certificate.                                                                                      | `string`   | `8760h`           |
| `certificate.renewBefore`     | How long before expiry cert-manager should renew the certificate.                                                               | `string`   | `720h`            |
| `certificate.dnsNames`        | Explicit list of DNS SANs to add to the certificate.                                                                            | `[]string` | `[]`              |
| `certificate.dnsNameSuffixes` | List of DNS name suffixes appended to auto-generated SANs.                                                                      | `[]string` | `[]`              |
| `certificate.usages`          | List of key usages for the certificate.                                                                                         | `[]string` | `[server auth]`   |
| `certificate.encoding`        | Private key encoding format (PKCS1 or PKCS8).                                                                                   | `string`   | `PKCS8`           |
| `certificate.algorithm`       | Key algorithm for the leaf certificate (ECDSA, RSA, or Ed25519).                                                                | `string`   | `ECDSA`           |
| `certificate.size`            | Key size in bits for the chosen algorithm (not used for Ed25519).                                                               | `int`      | `256`             |

