# Ingress-NGINX Controller

## Parameters

### Common parameters

| Name             | Description                                                       | Value   |
| ---------------- | ----------------------------------------------------------------- | ------- |
| `replicas`       | Number of ingress-nginx replicas                                  | `2`     |
| `externalIPs`    | List of externalIPs for service.                                  | `[]`    |
| `whitelist`      | List of client networks                                           | `[]`    |
| `clouflareProxy` | Restoring original visitor IPs when Cloudflare proxied is enabled | `false` |

