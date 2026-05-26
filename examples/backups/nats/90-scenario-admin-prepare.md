# Scenario: Cluster admin prepares NATS backups

A cluster administrator performs the cluster-level preparation once. Tenants
will then be able to back up and restore their NATS applications by referencing
the BackupClass created here, with no further admin action.

## Prerequisites

- A running Cozystack cluster with the backup-controller and
  backupstrategy-controller installed.
- `kubectl` access with permissions to create cluster-scoped
  `Job.strategy.backups.cozystack.io` and `BackupClass.backups.cozystack.io`.
- Reachable S3-compatible storage. The demo uses the in-cluster `Bucket` app
  (step 03); the tenant turns its coordinates into a `<app>-backup-s3` Secret
  that the strategy Pod reads (step 04 / 07).

## Steps

```bash
./01-create-strategy.sh        # Job strategy: stock nats-box image + a shell script
./02-create-backupclass.sh     # Maps Kind=NATS -> the strategy, with stream/natsUser params
```

## What gets created

| Resource | Scope | Purpose |
|---|---|---|
| `Job.strategy.backups.cozystack.io/nats-job` | Cluster | PodTemplateSpec for a `natsio/nats-box` Pod that runs `nats stream backup`/`restore` and moves a tarball to/from S3 with `curl --aws-sigv4` |
| `BackupClass/nats-backup` | Cluster | Maps `apps.cozystack.io/NATS` to the strategy. `parameters.stream` selects the JetStream stream; `parameters.natsUser` selects the NATS user to authenticate as |

## How the strategy template gets rendered

The Job driver renders the `Job.spec.template` PodTemplateSpec with this
context for every BackupJob/RestoreJob run:

| Variable | Source |
|---|---|
| `.Application` | The `NATS` (`apps.cozystack.io`) object referenced by the BackupJob (or `targetApplicationRef` on restore) — available to templates, though this one reads everything it needs from env |
| `.Release.Name` / `.Release.Namespace` | `metadata.name` / `metadata.namespace` of `.Application` |
| `.Parameters` | `BackupClass.spec.strategies[].parameters` (`stream`, `natsUser`); snapshotted onto the Backup's `driverMetadata` so restore re-renders with the same values |
| `.Mode` | `"backup"` or `"restore"` |
| `.Backup` | `{ Name, Namespace, ApplicationRef.{APIGroup,Kind,Name} }` (only for restore runs; `.ApplicationRef` points at the *source* release so to-copy restores read the source's S3 key) |

The same template renders both backup and restore Pods. The engine templates
each string field independently and cannot restructure the Pod per mode, so
the single nats-box container branches at runtime: `MODE` (from `{{ .Mode }}`)
selects `nats stream backup` + S3 `PUT` vs. S3 `GET` + `nats stream restore`.
