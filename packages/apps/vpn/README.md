# Managed VPN Service

A Virtual Private Network (VPN) is a critical tool for ensuring secure and private communication over the internet. Managed VPN Service simplifies the deployment and management of VPN server, enabling you to establish secure connections with ease.

- Clients: https://shadowsocks5.github.io/en/download/clients.html

## Deployment Details

The VPN Service is powered by the Outline Server, an advanced and user-friendly VPN solution. Internally known as "Shadowbox", which simplifies the process of setting up and sharing Shadowsocks servers. It operates by launching Shadowsocks instances on demand. Furthermore, Shadowbox is compatible with standard Shadowsocks clients, providing flexibility and ease of use for your VPN requirements.

- Docs: https://shadowsocks.org/
- Docs: https://github.com/Jigsaw-Code/outline-server/tree/master/src/shadowbox

## Parameters

### Common parameters

| Name       | Description                                     | Value   |
| ---------- | ----------------------------------------------- | ------- |
| `external` | Enable external access from outside the cluster | `false` |
| `replicas` | Number of VPN-server replicas                   | `2`     |

### Configuration parameters

| Name              | Description                                                                                                                                                                                                       | Value  |
| ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------ |
| `host`            | Host used to substitute into generated URLs                                                                                                                                                                       | `""`   |
| `users`           | Users configuration                                                                                                                                                                                               | `{}`   |
| `externalIPs`     | List of externalIPs for service.                                                                                                                                                                                  | `[]`   |
| `resources`       | Resources                                                                                                                                                                                                         | `{}`   |
| `resourcesPreset` | Set container resources according to one common preset (allowed values: none, nano, micro, small, medium, large, xlarge, 2xlarge). This is ignored if resources is set (resources is recommended for production). | `nano` |
