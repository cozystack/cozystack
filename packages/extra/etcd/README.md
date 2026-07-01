# Etcd-cluster

This chart renders an `EtcdCluster` (`etcd-operator.cozystack.io/v1alpha2`)
managed by the cozystack etcd-operator, plus the cert-manager CA chain and a
Kamaji `DataStore`. TLS uses secretRef mode: this chart's own cert-manager
`Certificate` resources issue the server, operator-client and peer certs from
the `etcd-issuer` / `etcd-peer-issuer` CA Issuers, and the `EtcdCluster` only
*references* the resulting Secrets via `serverSecretRef` /
`operatorClientSecretRef` / `peer.secretRef` (the operator itself mints
nothing). This is deliberate: `spec.tls` is immutable in v1alpha2 and
`etcd-migrate` adopts legacy clusters into exactly this secretRef shape, so the
chart must match it or every post-adoption reconcile is rejected. etcd runs
cert-only (`--client-cert-auth`, no password auth); consumers authenticate by
presenting a `commonName=root` client cert (`etcd-client-tls`) signed by the
same CA.

## Backups

Backups are driven by the Cozystack `BackupClass` flow: a `BackupJob` /
`RestoreJob` against the `Etcd` strategy (`strategy.backups.cozystack.io/v1alpha1`).
See `examples/backups/etcd/` for the end-to-end demo and
`internal/backupcontroller/etcdstrategy_controller.go` for the driver. The
driver materialises one `EtcdSnapshot` (`etcd-operator.cozystack.io/v1alpha2`)
per `BackupJob`; restore is in-place (the driver suspends this chart's
`HelmRelease`, deletes the live `EtcdCluster`, and re-creates it with
`spec.bootstrap.restore.source.s3` populated from the `Backup` artefact's
coordinates).

> The legacy `backup.*` values block and the `EtcdBackupSchedule` CRD no longer
> exist under the v1alpha2 operator — scheduled backups are expressed as a
> `BackupClass` / `Plan` instead.

## Parameters

### Common parameters

| Name               | Description                                                                                                                | Type       | Value    |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------- | ---------- | -------- |
| `version`          | etcd version (X.Y.Z) to run, set on the EtcdCluster's spec.version; the operator pulls etcd from its own configured image. | `string`   | `3.6.11` |
| `size`             | Persistent Volume size.                                                                                                    | `quantity` | `4Gi`    |
| `storageClass`     | StorageClass used to store the data.                                                                                       | `string`   | `""`     |
| `replicas`         | Number of etcd replicas.                                                                                                   | `int`      | `3`      |
| `resources`        | Resource configuration for etcd.                                                                                           | `object`   | `{}`     |
| `resources.cpu`    | Number of CPU cores allocated.                                                                                             | `quantity` | `1000m`  |
| `resources.memory` | Amount of memory allocated.                                                                                                | `quantity` | `512Mi`  |

