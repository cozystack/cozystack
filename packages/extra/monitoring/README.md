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

| Name                                      | Description                                                           | Type       | Value   |
| ----------------------------------------- | --------------------------------------------------------------------- | ---------- | ------- |
| `alerta`                                  | Configuration for the Alerta service.                                 | `object`   | `{}`    |
| `alerta.storage`                          | Persistent volume size for the database.                              | `string`   | `10Gi`  |
| `alerta.storageClassName`                 | StorageClass used for the database.                                   | `string`   | `""`    |
| `alerta.resources`                        | Resource configuration.                                               | `object`   | `{}`    |
| `alerta.resources.requests`               | Resource requests.                                                    | `object`   | `{}`    |
| `alerta.resources.requests.cpu`           | CPU request.                                                          | `quantity` | `100m`  |
| `alerta.resources.requests.memory`        | Memory request.                                                       | `quantity` | `256Mi` |
| `alerta.resources.limits`                 | Resource limits.                                                      | `object`   | `{}`    |
| `alerta.resources.limits.cpu`             | CPU limit.                                                            | `quantity` | `1`     |
| `alerta.resources.limits.memory`          | Memory limit.                                                         | `quantity` | `1Gi`   |
| `alerta.alerts`                           | Alert routing configuration.                                          | `object`   | `{}`    |
| `alerta.alerts.telegram`                  | Configuration for Telegram alerts.                                    | `object`   | `{}`    |
| `alerta.alerts.telegram.token`            | Telegram bot token.                                                   | `string`   | `""`    |
| `alerta.alerts.telegram.chatID`           | Telegram chat ID(s), separated by commas.                             | `string`   | `""`    |
| `alerta.alerts.telegram.disabledSeverity` | List of severities without alerts (e.g. ["informational","warning"]). | `[]string` | `[]`    |
| `alerta.alerts.slack`                     | Configuration for Slack alerts.                                       | `object`   | `{}`    |
| `alerta.alerts.slack.url`                 | Configuration uri for Slack alerts.                                   | `string`   | `""`    |
| `alerta.alerts.slack.disabledSeverity`    | List of severities without alerts (e.g. ["informational","warning"]). | `[]string` | `[]`    |


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


### Vmagent configuration

| Name                     | Description                              | Type     | Value |
| ------------------------ | ---------------------------------------- | -------- | ----- |
| `vmagent`                | Configuration for VictoriaMetrics Agent. | `object` | `{}`  |
| `vmagent.externalLabels` | External labels applied to all metrics.  | `object` | `{}`  |
| `vmagent.remoteWrite`    | Remote write configuration.              | `object` | `{}`  |


### OIDC authentication

| Name                               | Description                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Type       | Value  |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------- | ------ |
| `oidc`                             | OIDC authentication for the Grafana instance. See docs/oidc-grafana.md for the operator guide.                                                                                                                                                                                                                                                                                                                                                                                                                                                                            | `object`   | `{}`   |
| `oidc.mode`                        | Identity mode. `None`: no OIDC, only the `grafana-admin-password` Secret works. `System`: trust the platform `cozy` realm via a per-instance confidential client and audience binding; zero-config default. `CustomConfig`: trust a tenant-supplied issuer directly (BYO); `cozy` is not in the path.                                                                                                                                                                                                                                                                     | `string`   | `None` |
| `oidc.customConfig`                | Tenant-supplied `[auth.generic_oauth]` payload; consumed only when `mode: CustomConfig`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                  | `object`   | `{}`   |
| `oidc.customConfig.config`         | Inline map of `[auth.generic_oauth]` keys. Written verbatim into the Grafana CR `spec.config`. Mutually exclusive with `secretRef.name`.                                                                                                                                                                                                                                                                                                                                                                                                                                  | `object`   | `{}`   |
| `oidc.customConfig.secretRef`      | Reference to a Secret holding an `auth.ini` fragment; mounted under `/etc/grafana/oidc` and loaded via `GF_PATHS_CUSTOM_INI`.                                                                                                                                                                                                                                                                                                                                                                                                                                             | `object`   | `{}`   |
| `oidc.customConfig.secretRef.name` | Name of an existing Secret in the release namespace whose `auth.ini` key holds a ready-made `[auth.generic_oauth]` block. Mutually exclusive with `customConfig.config`.                                                                                                                                                                                                                                                                                                                                                                                                  | `string`   | `""`   |
| `oidc.users`                       | Users granted access to this Grafana instance's org. Each entry is reconciled into Grafana org membership + role by a chart-owned post-install/post-upgrade Job. Works for both `System` and `CustomConfig` modes; ignored when `mode: None`. Users not listed here get nothing (no default org role from group mapping).                                                                                                                                                                                                                                                 | `[]object` | `[]`   |
| `oidc.users[i].email`              | Email address matched against the `email` claim from the issuer. The chart pre-provisions a Grafana account with this email so the users-Job can set the org role before the operator's first login (`oauth_allow_insecure_email_lookup = true` glues the two together). Must match a conservative pattern: local part is `[A-Za-z0-9._%+-]+`, domain is `[A-Za-z0-9.-]+\.[A-Za-z]{2,}`. Whitespace, quoted literals, and unusual URL-reserved characters are rejected on schema level so the reconcile-Job's shell handling never has to defend against malformed input. | `string`   | `""`   |
| `oidc.users[i].role`               | Grafana org role to bind: `Admin`, `Editor`, or `Viewer`. Applied to the release's Grafana org (`Main Org.`); server-level `GrafanaAdmin` promotion is out of scope for Phase 1. Required — the users-Job POSTs `{loginOrEmail, role}` to `/api/orgs/1/users` and PATCHes `{role}` to converge across re-runs; omitting the field would render `role: "null"` into the body and fail with HTTP 400.                                                                                                                                                                     | `string`   | `{}`   |

