# Monitoring Hub

## Parameters

### Common parameters

| Name   | Description                                                                                               | Type     | Value |
| ------ | --------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `host` | The hostname used to access the grafana externally (defaults to 'grafana' subdomain for the tenant host). | `string` | ``    |

### Metrics storage configuration

| Name                                      | Description                                                    | Type               | Value  |
| ----------------------------------------- | -------------------------------------------------------------- | ------------------ | ------ |
| `metricsStorages`                         | Configuration of metrics storage instances                     | `[]metricsStorage` | `null` |
| `metricsStorages[].name`                  | Name of the storage instance                                   | `string`           |        |
| `metricsStorages[].retentionPeriod`       | Retention period for the metrics in the storage instance       | `string`           |        |
| `metricsStorages[].deduplicationInterval` | Deduplication interval for the metrics in the storage instance | `string`           |        |
| `metricsStorages[].storage`               | Persistent Volume size for the storage instance                | `string`           |        |
| `metricsStorages[].storageClassName`      | StorageClass used to store the data                            | `*string`          |        |
| `metricsStorages[].vminsert`              | Configuration for vminsert component of the storage instance   | `object`           |        |
| `metricsStorages[].vmselect`              | Configuration for vmselect component of the storage instance   | `object`           |        |
| `metricsStorages[].vmstorage`             | Configuration for vmstorage component of the storage instance  | `object`           |        |
| `metricsStorages[].vminsert.minAllowed`   | Minimum allowed resources for vminsert component               | `object`           |        |
| `metricsStorages[].vminsert.maxAllowed`   | Maximum allowed resources for vminsert component               | `object`           |        |
| `metricsStorages[].vmselect.minAllowed`   | Minimum allowed resources for vmselect component               | `object`           |        |
| `metricsStorages[].vmselect.maxAllowed`   | Maximum allowed resources for vmselect component               | `object`           |        |
| `metricsStorages[].vmstorage.minAllowed`  | Minimum allowed resources for vmstorage component              | `object`           |        |
| `metricsStorages[].vmstorage.maxAllowed`  | Maximum allowed resources for vmstorage component              | `object`           |        |
| `grafana.resources.requests.cpu`          | CPU resources                                                  | `*quantity`        |        |
| `grafana.resources.requests.memory`       | Memory resources                                               | `*quantity`        |        |

### Logs storage configuration

| Name                              | Description                                           | Type            | Value  |
| --------------------------------- | ----------------------------------------------------- | --------------- | ------ |
| `logsStorages`                    | Configuration of logs storage instances               | `[]logsStorage` | `null` |
| `logsStorages[].name`             | Name of the storage instance                          | `string`        |        |
| `logsStorages[].retentionPeriod`  | Retention period for the logs in the storage instance | `string`        |        |
| `logsStorages[].storage`          | Persistent Volume size for the storage instance       | `string`        |        |
| `logsStorages[].storageClassName` | StorageClass used to store the data                   | `*string`       |        |

### Alerta configuration

| Name                                      | Description                                                                         | Type     | Value  |
| ----------------------------------------- | ----------------------------------------------------------------------------------- | -------- | ------ |
| `alerta`                                  | Configuration for Alerta                                                            | `object` | `null` |
| `alerta.storage`                          | Persistent Volume size for alerta database                                          | `string` |        |
| `alerta.storageClassName`                 | StorageClass used to store the data                                                 | `string` |        |
| `alerta.resources`                        | Resources configuration for alerta                                                  | `object` |        |
| `alerta.resources.limits`                 | Resources limits for alerta                                                         | `object` |        |
| `alerta.resources.requests`               | Resources requests for alerta                                                       | `object` |        |
| `alerta.alerts`                           | Configuration for alerts                                                            | `object` |        |
| `alerta.alerts.telegram`                  | Configuration for Telegram alerts                                                   | `object` |        |
| `alerta.alerts.telegram.token`            | Telegram token for your bot                                                         | `string` |        |
| `alerta.alerts.telegram.chatID`           | Specify multiple ID's separated by comma. Get yours in https://t.me/chatid_echo_bot | `string` |        |
| `alerta.alerts.telegram.disabledSeverity` | List of severity without alerts, separated by comma like: "informational,warning"   | `string` |        |

### Grafana configuration

| Name                         | Description                                 | Type     | Value  |
| ---------------------------- | ------------------------------------------- | -------- | ------ |
| `grafana`                    | Configuration for Grafana                   | `object` | `null` |
| `grafana.db`                 |                                             | `object` |        |
| `grafana.db.size`            | Persistent Volume size for grafana database | `string` |        |
| `grafana.resources`          | Resources configuration for grafana         | `object` |        |
| `grafana.resources.limits`   | Resources limits for grafana                | `object` |        |
| `grafana.resources.requests` | Resources requests for grafana              | `object` |        |
