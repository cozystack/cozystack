# Tenant Kubernetes backup and restore

The `Kubernetes` strategy backs up a tenant Kubernetes application in two halves, taken one after the other, so a backup is crash-consistent at best:

- **etcd**: a Job dumps the cluster's key prefix from the tenant's etcd DataStore to the platform bucket. This is the cluster's API state.
- **Velero**: the control-plane and Talos PKI Secrets, and the DataVolumes and PVCs that back the cluster's PersistentVolumes. Worker system disks are left out; workers are recreated and join with the restored PKI.

The Backup also records the HelmReleases of the cluster and of its worker pools. A backup warns, through a `CertificatesExpiring` condition on the BackupJob, when a control-plane CA expires before the backup's TTL runs out or a leaf certificate expires within `certificateWarningThreshold` (720h).

## Admin: prepare

Nothing to do when the platform default backups are on: `cozy-default` routes `Kubernetes` to the `cozy-default-kubernetes` strategy.

## User: back up

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: my-cluster-backup
  namespace: tenant-foo
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Kubernetes
    name: my-cluster
  backupClassName: cozy-default
```

A `Plan` with the same `applicationRef` schedules it.

## User: restore

A restore always goes into the application the backup was taken from.

```yaml
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: my-cluster-restore
  namespace: tenant-foo
spec:
  backupRef:
    name: my-cluster-backup
  options:
    etcd: true     # restore the API state (default)
    volumes: true  # restore the PersistentVolume data (default)
```

- **Cluster deleted**: the restore rebuilds it. It restores the PKI Secrets and the volumes, recreates the cluster, writes the etcd state under the new control plane's prefix, and then recreates the worker pools, so the cluster comes back on its old state and its old CA. The cluster and pool HelmReleases are recreated with the spec recorded at backup time, so a backup taken before a platform upgrade that changed the application's values brings back the older values, which the upgraded chart may need migrated by hand.
- **Cluster live**: the restore rolls it back. It must be the cluster the backup was taken from: one deleted and created again under the same name has a new CA and service-account key, so the restore refuses it, and the way back to the backed-up cluster is to delete the application and run the restore again as a rebuild. It suspends the cluster's HelmReleases, stops the workers and the cluster-autoscaler when `volumes` are restored and then the control plane when `etcd` is, writes the selected halves back, and returns every replica count it changed. Workers are recreated, so expect the tenant's Pods to restart. Deleting the RestoreJob midway also returns the replica counts, unless its etcd write is under way.

Nothing is changed until the restore has confirmed that the backup's Velero half and etcd dump exist. If the etcd write fails partway, the control plane stays stopped and the RestoreJob points at the copy of the prefix it saved before writing; run a new RestoreJob to retry.

Restoring only one half leaves the other as it is now: `etcd` alone keeps the current volume contents, `volumes` alone keeps the current API objects.

The two halves differ in what a failed restore leaves behind. The etcd half saves the current prefix before it writes and can be retried from that copy. The volume half has no such copy: Velero cannot overwrite an existing claim, so a live-cluster restore deletes the current DataVolumes and PVCs before it restores them, and if the Velero restore then fails, the volume data exists only in the backup.

The etcd round trip scales with the number of keys under the cluster's prefix. The backup Job holds the whole dump in memory while it checks it, and the restore writes one key per `etcdctl` call: on a test cluster a 441-key restore took about 40 seconds including Pod start. At that rate the default `kubernetesBackupActiveDeadlineSeconds` (1h) covers a prefix of a few tens of thousands of keys. For a cluster with a larger API state, raise it together with the memory limit in `kubernetesBackupResources`.

## Walkthrough

```bash
./run-all.sh          # create, back up, roll back in place, rebuild after delete
./cleanup.sh
```

`NAMESPACE`, `CLUSTER_NAME` and `WORKER_DISK_SIZE` are read from the environment; see `00-helpers.sh`.
