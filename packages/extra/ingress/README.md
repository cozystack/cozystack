# Ingress-NGINX Controller

## Parameters

### Common parameters

| Name             | Description                                                       | Type        | Value   |
| ---------------- | ----------------------------------------------------------------- | ----------- | ------- |
| `replicas`       | Number of ingress-nginx replicas                                  | `int`       | `2`     |
| `whitelist`      | List of client networks                                           | `[]*string` | `[]`    |
| `clouflareProxy` | Restoring original visitor IPs when Cloudflare proxied is enabled | `bool`      | `false` |

