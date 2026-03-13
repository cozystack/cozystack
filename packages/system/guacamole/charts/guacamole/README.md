guacamole
=========

## TL;DR;

```console
$ helm repo add beryju https://charts.beryju.io
$ helm install guacamole beryju/guacamole
```

Apache Guacamole is a clientless remote desktop gateway. It supports standard protocols like VNC, RDP, and SSH.

This is a fork of https://artifacthub.io/packages/helm/halkeye/guacamole, but updated to support newer versions and more settings.

### Dependencies

This chart has a dependency on ``postgresql`` to be up and running _before_ this chart is deployed. The init-container will not fail if the ``postgresql`` service is not found.

Sample ``postgresql`` install which works with the defaults of this chart:
```console
helm install postgresql bitnami/postgresql \
 --set auth.username=guacamole \
 --set auth.password=password \
 --set auth.postgresPassword=password \
 --set auth.database=guacamole --wait
```

## Changelog

1.3.3 - Fixed ingress api and documented postgresql dependency

1.2.3 - Make guacamole run in ROOT context

0.2.3 - Add support for custom envs

0.2.2 - Update liveness and readiness probe path

0.2.1 - helm-docs doesn't add a tl;dr section, so add it manually

0.2.0 - Apparently I didn't actually use it before, i was running an old copy

* Fixed services to expose the ports properly
* Auto create the db on init if possible



## Chart Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` |  |
| dbcreation.image.pullPolicy | string | `"IfNotPresent"` |  |
| dbcreation.image.repository | string | `"bitnami/postgresql"` |  |
| dbcreation.image.tag | string | `"11.7.0-debian-10-r9"` |  |
| fullnameOverride | string | `""` |  |
| guacamole.image.pullPolicy | string | `"IfNotPresent"` |  |
| guacamole.image.repository | string | `"guacamole/guacamole"` |  |
| guacamole.image.tag | string | `"{{ .Chart.AppVersion }}"` |  |
| guacd.image.pullPolicy | string | `"IfNotPresent"` |  |
| guacd.image.repository | string | `"guacamole/guacd"` |  |
| guacd.image.tag | string | `"{{ .Chart.AppVersion }}"` |  |
| imagePullSecrets | list | `[]` |  |
| ingress.annotations | object | `{}` |  |
| ingress.enabled | bool | `false` |  |
| ingress.hosts[0].host | string | `"chart-example.local"` |  |
| ingress.hosts[0].paths | list | `[]` |  |
| ingress.tls | list | `[]` |  |
| nameOverride | string | `""` |  |
| nodeSelector | object | `{}` |  |
| podSecurityContext | object | `{}` |  |
| postgres.database | string | `"guacamole"` |  |
| postgres.hostname | string | `"postgresql"` |  |
| postgres.password | string | `"password"` |  |
| postgres.port | string | `"5432"` |  |
| postgres.user | string | `"guacamole"` |  |
| replicaCount | int | `1` |  |
| resources | object | `{}` |  |
| securityContext | object | `{}` |  |
| service.port | int | `80` |  |
| service.type | string | `"ClusterIP"` |  |
| serviceAccount.create | bool | `true` |  |
| serviceAccount.name | string | `nil` |  |
| tolerations | list | `[]` |  |

