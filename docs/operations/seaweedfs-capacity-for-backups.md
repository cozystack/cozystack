# SeaweedFS capacity for backups

A SeaweedFS instance starts with two volume servers of `volume.size: 10Gi` each and `replicationFactor: 2`, so every byte it stores lands on both servers and the instance holds a little under 10 GiB of objects. That is enough for occasional backups and restores. It is not enough for continuous WAL archiving from a Postgres cluster that writes steadily: WAL is archived around the clock and kept for the whole retention window, so the bucket grows until the window is full, and an instance sized by default runs out long before then.

A volume server stops taking writes in one of two ways. Once its free disk falls below `minFreeSpacePercent` (5%), which it checks every minute, every volume on it turns read-only and it takes no new volume, so every bucket with a copy there stops at once. Before that, it can run out of slots: the volumes that already exist keep taking writes until each reaches `master.volumeSizeLimitMB`, a bucket whose volumes are all full needs a new one, and a server with no room left for another volume of the size limit cannot host it. When no server can, the master logs `Not enough data nodes found` and every upload into that bucket fails. The slots can run out while the disk is still half empty: a volume server counts every volume it holds at the full size limit, so the nearly empty volumes of other buckets keep one bucket from growing. `barman-cloud-wal-archive` then fails on every segment, WAL segments stay on the Postgres volume, and the database goes down when that volume is full.

## Estimating what a bucket needs

For each Postgres cluster that archives into the instance, the bucket holds the WAL of the whole retention window plus every base backup inside it:

```text
bucket ≈ WAL per day × retention days + database size × (base backups in the window + 1)
```

The WAL rate of a running cluster, in bytes per day before compression, is roughly the number of segments it archived over a day times the 16 MiB segment size. The count also includes the small history and backup-label files, and the sum covers every pod that was primary during the day:

```promql
sum by (namespace, job) (increase(cnpg_pg_stat_archiver_archived_count{namespace="<namespace>"}[1d])) * 16 * 1024 * 1024
```

The platform's default backup strategy compresses both WAL and base backups with gzip, so the stored size is lower, by an amount that depends on the data; a cluster backed up to an object store of its own choosing stores them uncompressed unless its ObjectStore says otherwise. The default `retentionPolicy` is `30d`, so a cluster that writes 4 GB of WAL a day needs about 120 GB of WAL alone before its base backups are counted.

Each volume server must hold the sum of those buckets, since at `replicationFactor: 2` with two servers each server carries a full copy. Size `volume.size` so that this sum stays below 95% of it, and leave room on top for every bucket that is still growing: at `replicationFactor: 2` a grow claims three volumes of `master.volumeSizeLimitMB` on each server.

## Alerts that watch the instance

The generic `KubePersistentVolumeFillingUp` alert does not cover this. It pages below 3% free, while a volume server stops at 5% or, out of slots, with its disk half empty, and its early warning needs the disk to keep filling, which it stops doing once writes fail. Each instance carries its own rules instead:

- `SeaweedFSVolumeServerDiskLow` (warning): a volume server has less than 10% of its disk left before the `minFreeSpacePercent` reserve.
- `SeaweedFSVolumeServerFull` (warning): a volume server cannot host another volume, because it is at the reserve, where all its volumes are read-only, or has no slot left, where buckets whose volumes on it are full stop taking writes.
- `SeaweedFSWritesFailing` (critical): the master has failed to place a write in every 5-minute window for 15 minutes. Other writes may still succeed in between. The usual cause is a bucket with no writable volume left that the master cannot grow, whose uploads all fail; a steady stream of newly created buckets, each failing a few picks while its first volumes grow, looks the same.

On the Postgres side, `LastFailedArchiveTime` fires while WAL archiving keeps failing, and `KubePersistentVolumeFillingUp` fires as the WAL backlog fills the Postgres volume.

## Growing an instance

Raise `volume.size` on the SeaweedFS application. The upgrade expands each volume server's PVC in place, which needs a StorageClass that allows volume expansion. Postgres keeps retrying the segments that waited on its volume, so archiving catches up once the master can grow the bucket again.
