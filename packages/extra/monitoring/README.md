# Monitoring Hub

## Parameters

### Common parameters

| Name   | Description                                                                                               | Type     | Value |
| ------ | --------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `host` | The hostname used to access the grafana externally (defaults to 'grafana' subdomain for the tenant host). | `string` | `""`  |


### Metrics storage configuration

| Name                                             | Description                                                    | Type       | Value   |
| ------------------------------------------------ | -------------------------------------------------------------- | ---------- | ------- |
| `metricsStorages`                                | Configuration of metrics storage instances                     | `[]object` | `[...]` |
| `metricsStorages[i].name`                        | Name of the storage instance                                   | `string`   | `""`    |
| `metricsStorages[i].retentionPeriod`             | Retention period for the metrics in the storage instance       | `string`   | `""`    |
| `metricsStorages[i].deduplicationInterval`       | Deduplication interval for the metrics in the storage instance | `string`   | `""`    |
| `metricsStorages[i].storage`                     | Persistent Volume size for the storage instance                | `string`   | `""`    |
| `metricsStorages[i].storageClassName`            | StorageClass used to store the data                            | `*string`  | `null`  |
| `metricsStorages[i].vminsert`                    | Configuration for vminsert component of the storage instance   | `*object`  | `null`  |
| `metricsStorages[i].vminsert.minAllowed`         | Minimum allowed resources (requests) for each component        | `*object`  | `null`  |
| `metricsStorages[i].vminsert.minAllowed.cpu`     | CPU limit (maximum available value)                            | `*string`  | `null`  |
| `metricsStorages[i].vminsert.minAllowed.memory`  | Memory limit (maximum available value)                         | `*string`  | `null`  |
| `metricsStorages[i].vminsert.maxAllowed`         | Maximum allowed resources (limits) for each component          | `*object`  | `null`  |
| `metricsStorages[i].vminsert.maxAllowed.cpu`     | CPU request (minimal available value)                          | `*string`  | `null`  |
| `metricsStorages[i].vminsert.maxAllowed.memory`  | Memory request (minimal available value)                       | `*string`  | `null`  |
| `metricsStorages[i].vmselect`                    | Configuration for vmselect component of the storage instance   | `*object`  | `null`  |
| `metricsStorages[i].vmselect.minAllowed`         | Minimum allowed resources (requests) for each component        | `*object`  | `null`  |
| `metricsStorages[i].vmselect.minAllowed.cpu`     | CPU limit (maximum available value)                            | `*string`  | `null`  |
| `metricsStorages[i].vmselect.minAllowed.memory`  | Memory limit (maximum available value)                         | `*string`  | `null`  |
| `metricsStorages[i].vmselect.maxAllowed`         | Maximum allowed resources (limits) for each component          | `*object`  | `null`  |
| `metricsStorages[i].vmselect.maxAllowed.cpu`     | CPU request (minimal available value)                          | `*string`  | `null`  |
| `metricsStorages[i].vmselect.maxAllowed.memory`  | Memory request (minimal available value)                       | `*string`  | `null`  |
| `metricsStorages[i].vmstorage`                   | Configuration for vmstorage component of the storage instance  | `*object`  | `null`  |
| `metricsStorages[i].vmstorage.minAllowed`        | Minimum allowed resources (requests) for each component        | `*object`  | `null`  |
| `metricsStorages[i].vmstorage.minAllowed.cpu`    | CPU limit (maximum available value)                            | `*string`  | `null`  |
| `metricsStorages[i].vmstorage.minAllowed.memory` | Memory limit (maximum available value)                         | `*string`  | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed`        | Maximum allowed resources (limits) for each component          | `*object`  | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed.cpu`    | CPU request (minimal available value)                          | `*string`  | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed.memory` | Memory request (minimal available value)                       | `*string`  | `null`  |


### Logs storage configuration

| Name                               | Description                                           | Type       | Value   |
| ---------------------------------- | ----------------------------------------------------- | ---------- | ------- |
| `logsStorages`                     | Configuration of logs storage instances               | `[]object` | `[...]` |
| `logsStorages[i].name`             | Name of the storage instance                          | `string`   | `""`    |
| `logsStorages[i].retentionPeriod`  | Retention period for the logs in the storage instance | `string`   | `""`    |
| `logsStorages[i].storage`          | Persistent Volume size for the storage instance       | `string`   | `""`    |
| `logsStorages[i].storageClassName` | StorageClass used to store the data                   | `*string`  | `null`  |


### Alerta configuration

| Name                                      | Description                                                                         | Type      | Value   |
| ----------------------------------------- | ----------------------------------------------------------------------------------- | --------- | ------- |
| `alerta`                                  | Configuration for Alerta service                                                    | `object`  | `{}`    |
| `alerta.storage`                          | Persistent Volume size for the database                                             | `string`  | `10Gi`  |
| `alerta.storageClassName`                 | StorageClass used to store the data                                                 | `string`  | `""`    |
| `alerta.resources`                        | Resources configuration                                                             | `*object` | `null`  |
| `alerta.resources.requests`               |                                                                                     | `*object` | `null`  |
| `alerta.resources.requests.cpu`           | CPU request (minimal available value)                                               | `*string` | `100m`  |
| `alerta.resources.requests.memory`        | Memory request (minimal available value)                                            | `*string` | `256Mi` |
| `alerta.resources.limits`                 |                                                                                     | `*object` | `null`  |
| `alerta.resources.limits.cpu`             | CPU limit (maximum available value)                                                 | `*string` | `1`     |
| `alerta.resources.limits.memory`          | Memory limit (maximum available value)                                              | `*string` | `1Gi`   |
| `alerta.alerts`                           | Configuration for alerts                                                            | `object`  | `{}`    |
| `alerta.alerts.telegram`                  | Configuration for Telegram alerts                                                   | `object`  | `{}`    |
| `alerta.alerts.telegram.token`            | Telegram token for your bot                                                         | `string`  | `""`    |
| `alerta.alerts.telegram.chatID`           | Specify multiple ID's separated by comma. Get yours in https://t.me/chatid_echo_bot | `string`  | `""`    |
| `alerta.alerts.telegram.disabledSeverity` | List of severity without alerts, separated by comma like: "informational,warning"   | `string`  | `""`    |


### Grafana configuration

| Name                                | Description                              | Type      | Value   |
| ----------------------------------- | ---------------------------------------- | --------- | ------- |
| `grafana`                           | Configuration for Grafana                | `object`  | `{}`    |
| `grafana.db`                        | Database configuration                   | `object`  | `{}`    |
| `grafana.db.size`                   | Persistent Volume size for the database  | `string`  | `10Gi`  |
| `grafana.resources`                 | Resources configuration                  | `*object` | `null`  |
| `grafana.resources.requests`        |                                          | `*object` | `null`  |
| `grafana.resources.requests.cpu`    | CPU request (minimal available value)    | `*string` | `100m`  |
| `grafana.resources.requests.memory` | Memory request (minimal available value) | `*string` | `256Mi` |
| `grafana.resources.limits`          |                                          | `*object` | `null`  |
| `grafana.resources.limits.cpu`      | CPU limit (maximum available value)      | `*string` | `1`     |
| `grafana.resources.limits.memory`   | Memory limit (maximum available value)   | `*string` | `1Gi`   |

