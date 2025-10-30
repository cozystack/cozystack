# Ingress-NGINX Controller

## Parameters

### Common parameters

| Name               | Description                                                                                                                             | Type       | Value   |
| ------------------ | --------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------- |
| `replicas`         | Number of ingress-nginx replicas.                                                                                                       | `int`      | `2`     |
| `whitelist`        | List of client networks.                                                                                                                | `[]string` | `[]`    |
| `cloudflareProxy`  | Restoring original visitor IPs when Cloudflare proxied is enabled.                                                                      | `bool`     | `false` |
| `resources`        | Explicit CPU and memory configuration for each ingress-nginx replica. When omitted, the preset defined in `resourcesPreset` is applied. | `object`   | `{}`    |
| `resources.cpu`    | CPU available to each replica.                                                                                                          | `quantity` | `""`    |
| `resources.memory` | Memory (RAM) available to each replica.                                                                                                 | `quantity` | `""`    |
| `resourcesPreset`  | Default sizing preset used when `resources` is omitted.                                                                                 | `string`   | `micro` |

