# Monitoring Hub

## Parameters

### Common parameters

| Name   | Description                                                                                               | Type     | Value |
| ------ | --------------------------------------------------------------------------------------------------------- | -------- | ----- |
| `host` | The hostname used to access the grafana externally (defaults to 'grafana' subdomain for the tenant host). | `string` | `""`  |


### Metrics storage configuration

| Name                                             | Description                                                    | Type        | Value   |
| ------------------------------------------------ | -------------------------------------------------------------- | ----------- | ------- |
| `metricsStorages`                                | Configuration of metrics storage instances                     | `[]object`  | `[...]` |
| `metricsStorages[i].name`                        | Name of the storage instance                                   | `string`    | `""`    |
| `metricsStorages[i].retentionPeriod`             | Retention period for the metrics in the storage instance       | `string`    | `""`    |
| `metricsStorages[i].deduplicationInterval`       | Deduplication interval for the metrics in the storage instance | `string`    | `""`    |
| `metricsStorages[i].storage`                     | Persistent Volume size for the storage instance                | `string`    | `""`    |
| `metricsStorages[i].storageClassName`            | StorageClass used to store the data                            | `*string`   | `null`  |
| `metricsStorages[i].vminsert`                    | Configuration for vminsert component of the storage instance   | `*object`   | `null`  |
| `metricsStorages[i].vminsert.minAllowed`         | Requests (minimum allowed/available resources)                 | `*object`   | `null`  |
| `metricsStorages[i].vminsert.minAllowed.cpu`     | CPU request (minimum available CPU)                            | `*quantity` | `null`  |
| `metricsStorages[i].vminsert.minAllowed.memory`  | Memory request (minimum available memory)                      | `*quantity` | `null`  |
| `metricsStorages[i].vminsert.maxAllowed`         | Limits (maximum allowed/available resources )                  | `*object`   | `null`  |
| `metricsStorages[i].vminsert.maxAllowed.cpu`     | CPU limit (maximum available CPU)                              | `*quantity` | `null`  |
| `metricsStorages[i].vminsert.maxAllowed.memory`  | Memory limit (maximum available memory)                        | `*quantity` | `null`  |
| `metricsStorages[i].vmselect`                    | Configuration for vmselect component of the storage instance   | `*object`   | `null`  |
| `metricsStorages[i].vmselect.minAllowed`         | Requests (minimum allowed/available resources)                 | `*object`   | `null`  |
| `metricsStorages[i].vmselect.minAllowed.cpu`     | CPU request (minimum available CPU)                            | `*quantity` | `null`  |
| `metricsStorages[i].vmselect.minAllowed.memory`  | Memory request (minimum available memory)                      | `*quantity` | `null`  |
| `metricsStorages[i].vmselect.maxAllowed`         | Limits (maximum allowed/available resources )                  | `*object`   | `null`  |
| `metricsStorages[i].vmselect.maxAllowed.cpu`     | CPU limit (maximum available CPU)                              | `*quantity` | `null`  |
| `metricsStorages[i].vmselect.maxAllowed.memory`  | Memory limit (maximum available memory)                        | `*quantity` | `null`  |
| `metricsStorages[i].vmstorage`                   | Configuration for vmstorage component of the storage instance  | `*object`   | `null`  |
| `metricsStorages[i].vmstorage.minAllowed`        | Requests (minimum allowed/available resources)                 | `*object`   | `null`  |
| `metricsStorages[i].vmstorage.minAllowed.cpu`    | CPU request (minimum available CPU)                            | `*quantity` | `null`  |
| `metricsStorages[i].vmstorage.minAllowed.memory` | Memory request (minimum available memory)                      | `*quantity` | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed`        | Limits (maximum allowed/available resources )                  | `*object`   | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed.cpu`    | CPU limit (maximum available CPU)                              | `*quantity` | `null`  |
| `metricsStorages[i].vmstorage.maxAllowed.memory` | Memory limit (maximum available memory)                        | `*quantity` | `null`  |


### Logs storage configuration

| Name                               | Description                                           | Type       | Value   |
| ---------------------------------- | ----------------------------------------------------- | ---------- | ------- |
| `logsStorages`                     | Configuration of logs storage instances               | `[]object` | `[...]` |
| `logsStorages[i].name`             | Name of the storage instance                          | `string`   | `""`    |
| `logsStorages[i].retentionPeriod`  | Retention period for the logs in the storage instance | `string`   | `""`    |
| `logsStorages[i].storage`          | Persistent Volume size for the storage instance       | `string`   | `""`    |
| `logsStorages[i].storageClassName` | StorageClass used to store the data                   | `*string`  | `null`  |


### Alerta configuration

| Name                                      | Description                                                                         | Type        | Value   |
| ----------------------------------------- | ----------------------------------------------------------------------------------- | ----------- | ------- |
| `alerta`                                  | Configuration for Alerta service                                                    | `object`    | `{}`    |
| `alerta.storage`                          | Persistent Volume size for the database                                             | `*string`   | `10Gi`  |
| `alerta.storageClassName`                 | StorageClass used to store the data                                                 | `*string`   | `""`    |
| `alerta.resources`                        | Resources configuration                                                             | `*object`   | `null`  |
| `alerta.resources.requests`               |                                                                                     | `*object`   | `null`  |
| `alerta.resources.requests.cpu`           | CPU request (minimum available CPU)                                                 | `*quantity` | `100m`  |
| `alerta.resources.requests.memory`        | Memory request (minimum available memory)                                           | `*quantity` | `256Mi` |
| `alerta.resources.limits`                 |                                                                                     | `*object`   | `null`  |
| `alerta.resources.limits.cpu`             | CPU limit (maximum available CPU)                                                   | `*quantity` | `1`     |
| `alerta.resources.limits.memory`          | Memory limit (maximum available memory)                                             | `*quantity` | `1Gi`   |
| `alerta.alerts`                           | Configuration for alerts                                                            | `*object`   | `null`  |
| `alerta.alerts.telegram`                  | Configuration for Telegram alerts                                                   | `*object`   | `null`  |
| `alerta.alerts.telegram.token`            | Telegram token for your bot                                                         | `string`    | `""`    |
| `alerta.alerts.telegram.chatID`           | Specify multiple ID's separated by comma. Get yours in https://t.me/chatid_echo_bot | `string`    | `""`    |
| `alerta.alerts.telegram.disabledSeverity` | List of severity without alerts, separated by comma like: "informational,warning"   | `string`    | `""`    |


### Grafana configuration

| Name                                | Description                               | Type        | Value   |
| ----------------------------------- | ----------------------------------------- | ----------- | ------- |
| `grafana`                           | Configuration for Grafana                 | `object`    | `{}`    |
| `grafana.db`                        | Database configuration                    | `*object`   | `null`  |
| `grafana.db.size`                   | Persistent Volume size for the database   | `*string`   | `10Gi`  |
| `grafana.resources`                 | Resources configuration                   | `*object`   | `null`  |
| `grafana.resources.requests`        |                                           | `*object`   | `null`  |
| `grafana.resources.requests.cpu`    | CPU request (minimum available CPU)       | `*quantity` | `100m`  |
| `grafana.resources.requests.memory` | Memory request (minimum available memory) | `*quantity` | `256Mi` |
| `grafana.resources.limits`          |                                           | `*object`   | `null`  |
| `grafana.resources.limits.cpu`      | CPU limit (maximum available CPU)         | `*quantity` | `1`     |
| `grafana.resources.limits.memory`   | Memory limit (maximum available memory)   | `*quantity` | `1Gi`   |

