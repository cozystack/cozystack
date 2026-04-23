# Cozystack Tenant Gateway

Per-tenant Gateway API Gateway backed by Cilium. Installed automatically when `tenant.spec.gateway=true` on the publishing tenant.

## Parameters

### Common parameters

| Name               | Description                                                                                                      | Type     | Value    |
| ------------------ | ---------------------------------------------------------------------------------------------------------------- | -------- | -------- |
| `gatewayClassName` | GatewayClass to attach the tenant Gateway to. Must exist cluster-wide. Default matches the Cilium-managed class. | `string` | `cilium` |

