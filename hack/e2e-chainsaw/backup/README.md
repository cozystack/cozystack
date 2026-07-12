# Backup E2E (disabled / opt-in)

`chainsaw-test.yaml.disabled` is a Chainsaw port of the former `hack/e2e-apps/backup.bats.disabled`. It boots a VirtualMachine, drives a `BackupJob` through the default Velero strategy, and verifies the resulting Velero backup landed in S3.

It is **not** part of the automated suite — the `.disabled` suffix keeps it out of chainsaw's default `chainsaw-test.yaml` discovery, and `hack/select-e2e.sh` does not enumerate it as a selectable suite. This preserves the previous `backup.bats.disabled` behavior: the test is heavy (a full VM plus a Velero backup and S3 round-trip) and depends on backup infrastructure the standard e2e sandbox install does not guarantee.

## Preconditions

Run it only against a cozystack cluster that has backups enabled, i.e. with the `backup-controller`, `backupstrategy-controller`, and Velero installed, and a `velero-strategy-default` Velero strategy present (the install provides it). The test uses the `tenant-test` namespace, as configured in `hack/e2e-chainsaw/.chainsaw.yaml`.

## Running

```sh
chainsaw test --test-file chainsaw-test.yaml.disabled hack/e2e-chainsaw/backup
```

To enable it permanently, rename it to `chainsaw-test.yaml` — it then joins the full suite and becomes selectable by Test Impact Analysis.

## Cleanup

Chainsaw deletes the Bucket / VirtualMachine / BackupJob it applied. The Velero `Backup`, `BackupStorageLocation`, and credentials secret in the `cozy-velero` namespace are created by the backup-controller (not chainsaw), so prune them by hand after an opt-in run.
