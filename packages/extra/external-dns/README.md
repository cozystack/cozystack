# External DNS

## Parameters

### Common parameters

| Name               | Description                                                                        | Type       | Value         |
| ------------------ | ---------------------------------------------------------------------------------- | ---------- | ------------- |
| `provider`         | DNS provider name.                                                                 | `string`   | `{}`          |
| `domainFilters`    | List of domains this external-dns instance can manage.                             | `[]string` | `[]`          |
| `policy`           | How DNS records are synchronized.                                                  | `string`   | `upsert-only` |
| `extraArgs`        | Extra arguments for external-dns.                                                  | `[]string` | `[]`          |
| `gatewayAPI`       | Enable Gateway API HTTPRoute as a source for DNS records.                          | `bool`     | `false`       |
| `annotationPrefix` | Custom annotation prefix for external-dns (useful for running multiple instances). | `string`   | `""`          |


### Cloudflare

| Name         | Description                      | Type     | Value                                                       |
| ------------ | -------------------------------- | -------- | ----------------------------------------------------------- |
| `cloudflare` | Cloudflare provider credentials. | `object` | `{"apiEmail":"","apiKey":"","apiToken":"","proxied":false}` |


### AWS

| Name  | Description                       | Type     | Value                                                               |
| ----- | --------------------------------- | -------- | ------------------------------------------------------------------- |
| `aws` | AWS Route53 provider credentials. | `object` | `{"accessKeyId":"","region":"","secretAccessKey":"","zoneType":""}` |


### Azure

| Name    | Description                     | Type     | Value                                                                                          |
| ------- | ------------------------------- | -------- | ---------------------------------------------------------------------------------------------- |
| `azure` | Azure DNS provider credentials. | `object` | `{"aadClientId":"","aadClientSecret":"","resourceGroup":"","subscriptionId":"","tenantId":""}` |


### Google

| Name     | Description                            | Type     | Value                                   |
| -------- | -------------------------------------- | -------- | --------------------------------------- |
| `google` | Google Cloud DNS provider credentials. | `object` | `{"project":"","serviceAccountKey":""}` |


### DigitalOcean

| Name           | Description                            | Type     | Value          |
| -------------- | -------------------------------------- | -------- | -------------- |
| `digitalocean` | DigitalOcean DNS provider credentials. | `object` | `{"token":""}` |


### Linode

| Name     | Description                      | Type     | Value          |
| -------- | -------------------------------- | -------- | -------------- |
| `linode` | Linode DNS provider credentials. | `object` | `{"token":""}` |


### OVH

| Name  | Description                   | Type     | Value                                                                         |
| ----- | ----------------------------- | -------- | ----------------------------------------------------------------------------- |
| `ovh` | OVH DNS provider credentials. | `object` | `{"applicationKey":"","applicationSecret":"","consumerKey":"","endpoint":""}` |


### Exoscale

| Name       | Description                        | Type     | Value                          |
| ---------- | ---------------------------------- | -------- | ------------------------------ |
| `exoscale` | Exoscale DNS provider credentials. | `object` | `{"apiKey":"","apiSecret":""}` |


### GoDaddy

| Name      | Description                       | Type     | Value                          |
| --------- | --------------------------------- | -------- | ------------------------------ |
| `godaddy` | GoDaddy DNS provider credentials. | `object` | `{"apiKey":"","apiSecret":""}` |


### Resources

| Name               | Description                                                                                              | Type       | Value  |
| ------------------ | -------------------------------------------------------------------------------------------------------- | ---------- | ------ |
| `resources`        | Explicit CPU and memory configuration. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`   |
| `resources.cpu`    | CPU available to each replica.                                                                           | `quantity` | `""`   |
| `resources.memory` | Memory (RAM) available to each replica.                                                                  | `quantity` | `""`   |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                  | `string`   | `nano` |

