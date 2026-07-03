# Scenario: tenant takes an ad-hoc Etcd backup

This narrative walks through what a tenant does to capture a one-shot
snapshot of their `apps.cozystack.io/Etcd` application via the
Cozystack `BackupClass` flow.

## 1. Provision storage (`02-create-bucket.sh`)

The tenant creates an `apps.cozystack.io/Bucket` in their namespace,
which materialises a COSI bucket + access credentials. The script
extracts the BucketInfo, mints an `<etcd-app-name>-etcd-backup-creds`
Secret (the chart-managed `etcd-operator` will mount it on the snapshot
Job to provide `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` to
`etcdctl`'s S3 client), and creates a cluster-scoped `BackupClass` that
points at the cluster-shared `Etcd` strategy with the resolved bucket
coordinates baked into `parameters`.

## 2. Deploy the source Etcd (`03-create-etcd-src.sh`)

The tenant applies an `apps.cozystack.io/Etcd` named `etcd`. The
HelmRelease materialises an `etcd-operator.cozystack.io/EtcdCluster` and three
members. The script writes a sentinel key under `etcdctl` so step 05
can witness the round-trip.

## 3. Submit the BackupJob (`04-create-backupjob.sh`)

A `BackupJob` references the `BackupClass` and the source `Etcd`. The
driver resolves the strategy, renders its template against the live
application and class parameters, then materialises one
`etcd-operator.cozystack.io/EtcdSnapshot` CR per `BackupJob`. The etcd-operator's
controller runs a one-shot Job that snapshots etcd and uploads the
file to S3. On `phase=Complete`, the driver creates a Cozystack
`Backup` artefact with the S3 coordinates baked into
`spec.driverMetadata` and `status.underlyingResources`. The
`BackupJob` flips to `phase=Succeeded`.

## What the tenant does NOT do

- Author `EtcdSnapshot` CRs by hand. The driver owns the lifecycle.
- Manage the underlying S3 credentials. The driver consumes them from
  the per-app Secret the bucket flow already produced.
- Track snapshot retention via cron. A `Plan` resource (not part of
  this demo) hooks into the same `BackupJob` lifecycle if recurring
  backups are needed.
