# Monitoring Hub

## Parameters

### Common parameters

| Name   | Description                                                                                           | Type     | Value |
| ------ | ----------------------------------------------------------------------------------------------------- | -------- | ----- |
| `host` | The hostname used to access Grafana externally (defaults to 'grafana' subdomain for the tenant host). | `string` | `""`  |


### Metrics storage configuration

| Name                                             | Description                                 | Type       | Value   |
| ------------------------------------------------ | ------------------------------------------- | ---------- | ------- |
| `metricsStorages`                                | Configuration of metrics storage instances. | `[]object` | `[...]` |
| `metricsStorages[i].name`                        | Name of the storage instance.               | `string`   | `""`    |
| `metricsStorages[i].retentionPeriod`             | Retention period for metrics.               | `string`   | `""`    |
| `metricsStorages[i].deduplicationInterval`       | Deduplication interval for metrics.         | `string`   | `""`    |
| `metricsStorages[i].storage`                     | Persistent volume size.                     | `string`   | `10Gi`  |
| `metricsStorages[i].storageClassName`            | StorageClass used for the data.             | `string`   | `""`    |
| `metricsStorages[i].vminsert`                    | Configuration for vminsert.                 | `object`   | `{}`    |
| `metricsStorages[i].vminsert.minAllowed`         | Minimum guaranteed resources.               | `object`   | `{}`    |
| `metricsStorages[i].vminsert.minAllowed.cpu`     | CPU request.                                | `quantity` | `""`    |
| `metricsStorages[i].vminsert.minAllowed.memory`  | Memory request.                             | `quantity` | `""`    |
| `metricsStorages[i].vminsert.maxAllowed`         | Maximum allowed resources.                  | `object`   | `{}`    |
| `metricsStorages[i].vminsert.maxAllowed.cpu`     | CPU limit.                                  | `quantity` | `""`    |
| `metricsStorages[i].vminsert.maxAllowed.memory`  | Memory limit.                               | `quantity` | `""`    |
| `metricsStorages[i].vmselect`                    | Configuration for vmselect.                 | `object`   | `{}`    |
| `metricsStorages[i].vmselect.minAllowed`         | Minimum guaranteed resources.               | `object`   | `{}`    |
| `metricsStorages[i].vmselect.minAllowed.cpu`     | CPU request.                                | `quantity` | `""`    |
| `metricsStorages[i].vmselect.minAllowed.memory`  | Memory request.                             | `quantity` | `""`    |
| `metricsStorages[i].vmselect.maxAllowed`         | Maximum allowed resources.                  | `object`   | `{}`    |
| `metricsStorages[i].vmselect.maxAllowed.cpu`     | CPU limit.                                  | `quantity` | `""`    |
| `metricsStorages[i].vmselect.maxAllowed.memory`  | Memory limit.                               | `quantity` | `""`    |
| `metricsStorages[i].vmstorage`                   | Configuration for vmstorage.                | `object`   | `{}`    |
| `metricsStorages[i].vmstorage.minAllowed`        | Minimum guaranteed resources.               | `object`   | `{}`    |
| `metricsStorages[i].vmstorage.minAllowed.cpu`    | CPU request.                                | `quantity` | `""`    |
| `metricsStorages[i].vmstorage.minAllowed.memory` | Memory request.                             | `quantity` | `""`    |
| `metricsStorages[i].vmstorage.maxAllowed`        | Maximum allowed resources.                  | `object`   | `{}`    |
| `metricsStorages[i].vmstorage.maxAllowed.cpu`    | CPU limit.                                  | `quantity` | `""`    |
| `metricsStorages[i].vmstorage.maxAllowed.memory` | Memory limit.                               | `quantity` | `""`    |


### Logs storage configuration

| Name                               | Description                              | Type       | Value        |
| ---------------------------------- | ---------------------------------------- | ---------- | ------------ |
| `logsStorages`                     | Configuration of logs storage instances. | `[]object` | `[...]`      |
| `logsStorages[i].name`             | Name of the storage instance.            | `string`   | `""`         |
| `logsStorages[i].retentionPeriod`  | Retention period for logs.               | `string`   | `1`          |
| `logsStorages[i].storage`          | Persistent volume size.                  | `string`   | `10Gi`       |
| `logsStorages[i].storageClassName` | StorageClass used to store the data.     | `string`   | `replicated` |


### Alerta configuration

| Name                                      | Description                                                       | Type       | Value   |
| ----------------------------------------- | ----------------------------------------------------------------- | ---------- | ------- |
| `alerta`                                  | Configuration for the Alerta service.                             | `object`   | `{}`    |
| `alerta.storage`                          | Persistent volume size for the database.                          | `string`   | `10Gi`  |
| `alerta.storageClassName`                 | StorageClass used for the database.                               | `string`   | `""`    |
| `alerta.resources`                        | Resource configuration.                                           | `object`   | `{}`    |
| `alerta.resources.requests`               | Resource requests.                                                | `object`   | `{}`    |
| `alerta.resources.requests.cpu`           | CPU request.                                                      | `quantity` | `100m`  |
| `alerta.resources.requests.memory`        | Memory request.                                                   | `quantity` | `256Mi` |
| `alerta.resources.limits`                 | Resource limits.                                                  | `object`   | `{}`    |
| `alerta.resources.limits.cpu`             | CPU limit.                                                        | `quantity` | `1`     |
| `alerta.resources.limits.memory`          | Memory limit.                                                     | `quantity` | `1Gi`   |
| `alerta.alerts`                           | Alert routing configuration.                                      | `object`   | `{}`    |
| `alerta.alerts.telegram`                  | Configuration for Telegram alerts.                                | `object`   | `{}`    |
| `alerta.alerts.telegram.token`            | Telegram bot token.                                               | `string`   | `""`    |
| `alerta.alerts.telegram.chatID`           | Telegram chat ID(s), separated by commas.                         | `string`   | `""`    |
| `alerta.alerts.telegram.disabledSeverity` | List of severities without alerts (e.g. "informational,warning"). | `string`   | `""`    |
| `alerta.alerts.slack`                     | Configuration for Slack alerts.                                   | `object`   | `{}`    |
| `alerta.alerts.slack.url`                 | Configuration uri for Slack alerts.                               | `string`   | `""`    |


### Grafana configuration

| Name                                | Description                              | Type       | Value   |
| ----------------------------------- | ---------------------------------------- | ---------- | ------- |
| `grafana`                           | Configuration for Grafana.               | `object`   | `{}`    |
| `grafana.db`                        | Database configuration.                  | `object`   | `{}`    |
| `grafana.db.size`                   | Persistent volume size for the database. | `string`   | `10Gi`  |
| `grafana.resources`                 | Resource configuration.                  | `object`   | `{}`    |
| `grafana.resources.requests`        | Resource requests.                       | `object`   | `{}`    |
| `grafana.resources.requests.cpu`    | CPU request.                             | `quantity` | `100m`  |
| `grafana.resources.requests.memory` | Memory request.                          | `quantity` | `256Mi` |
| `grafana.resources.limits`          | Resource limits.                         | `object`   | `{}`    |
| `grafana.resources.limits.cpu`      | CPU limit.                               | `quantity` | `1`     |
| `grafana.resources.limits.memory`   | Memory limit.                            | `quantity` | `1Gi`   |

