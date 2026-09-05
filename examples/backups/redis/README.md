# Redis backup / restore demo

Proves an honest data round-trip for a Cozystack-managed `Redis` (spotahome `RedisFailover`) through the platform system bucket: a sentinel marker key is written, backed up, and asserted to return byte-for-byte after both restore flows.

The spotahome operator ships no backup CRD, so the `strategy.backups.cozystack.io/Redis` driver runs a one-shot Job that dumps an RDB snapshot to S3 and replays it on restore (RDB → RESP via `redis-cli --pipe`). This is the `cozy-default-redis` strategy the `cozy-default` BackupClass binds `Redis` to.

## Prerequisites

- A Cozystack cluster with the `backupstrategy-controller` running and its system bucket provisioned, so `cozy-default` (and the `cozy-default-redis` strategy) are present and the controller can project `cozy-backups-creds` into the app namespace.
- `kubectl` pointed at the cluster; the tenant namespace defaults to `tenant-test` (override with `NAMESPACE`).

## Run

```bash
./run-all.sh            # steps 01-04: provision, back up, restore in-place, restore to-copy
SKIP_RESTORE=1 ./run-all.sh   # stop after a successful backup
./cleanup.sh            # remove everything the demo created
```

Or step through them:

| Step | Script | What it does |
|------|--------|--------------|
| 01 | `01-create-redis.sh` | Provision `Redis`, seed the marker on the master |
| 02 | `02-create-backupjob.sh` | `BackupJob` (`backupClassName: cozy-default`) → RDB dump to S3 |
| 03 | `03-restore-in-place.sh` | Corrupt the marker, `RestoreJob` back into the source, assert it returns |
| 04 | `04-restore-to-copy.sh` | `RestoreJob` with `targetApplicationRef` into a fresh copy, assert the marker landed |

## Notes

- **Master discovery** goes through the operator's sentinel Service `rfs-redis-<name>` (master name `mymaster`), so writes and the dump/replay always target the current master even after a failover.
- **Faithful restore.** The restore loads the RDB into a throwaway local redis and replays what a real engine emits, so all logical databases (`SELECT` per db), stream last-ids and consumer groups survive — a merge onto db0 does not happen.
- **Endpoint TLS.** The projected `endpoint` in `cozy-backups-creds` is always scheme-stripped, so the strategy prepends `https://` and the transport is always TLS (there is no `http://` path). It verifies against the image's system CA store by default — the platform bucket's ACME cert needs nothing more — honours a private CA from `backupStorage.endpointCASecretName` when set, and skips verification only when `backupStorage.insecureSkipTLSVerify: true` is set explicitly.
- **No PITR.** RDB is a discrete snapshot; the engine ships no continuous log to replay to an arbitrary time, so `spec.options.recoveryTime` is not supported (the etcd-like case).
