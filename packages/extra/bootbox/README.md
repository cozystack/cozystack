# BootBox

## Parameters

### Common parameters

| Name                      | Description                                           | Type       | Value   |
| ------------------------- | ----------------------------------------------------- | ---------- | ------- |
| `whitelistHTTP`           | Secure HTTP by enabling client networks whitelisting. | `bool`     | `true`  |
| `whitelist`               | List of client networks.                              | `[]string` | `[]`    |
| `machines`                | Configuration of physical machine instances.          | `[]object` | `[]`    |
| `machines[i].hostname`    | Hostname.                                             | `string`   | `""`    |
| `machines[i].arch`        | Architecture.                                         | `string`   | `""`    |
| `machines[i].ip`          | IP address configuration.                             | `object`   | `{}`    |
| `machines[i].ip.address`  | IP address.                                           | `string`   | `""`    |
| `machines[i].ip.gateway`  | IP gateway.                                           | `string`   | `""`    |
| `machines[i].ip.netmask`  | Netmask.                                              | `string`   | `""`    |
| `machines[i].leaseTime`   | Lease time.                                           | `int`      | `0`     |
| `machines[i].mac`         | MAC addresses.                                        | `[]string` | `[]`    |
| `machines[i].nameServers` | Name servers.                                         | `[]string` | `[]`    |
| `machines[i].timeServers` | Time servers.                                         | `[]string` | `[]`    |
| `machines[i].uefi`        | UEFI.                                                 | `bool`     | `false` |

