# Scenario: User Restores a VM

This scenario demonstrates two restore methods: in-place restore (rollback the
VM to a previous state) and cross-namespace restore (create a copy of the VM in
a different namespace).

## Prerequisites

- A completed backup from [scenario-user-backup.md](scenario-user-backup.md)
  (the `test-backup` Backup object exists in the tenant namespace).
- `kubectl` access scoped to the tenant namespace.

## Method 1: In-Place Restore

```bash
./06-restore-in-place.sh
```

Creates a `RestoreJob` that restores the `test` VMInstance back to the state
captured in the `test-backup` Backup. The `targetApplicationRef` points to the
same application as the original backup.

The backup controller performs the following steps automatically:

1. **Suspends HelmReleases** — prevents Flux from reconciling the VM and its
   disks during restore.
2. **Halts the VirtualMachine** — sets `runStrategy: Halted` and waits for the
   VirtualMachineInstance (launcher pod) to terminate.
3. **Renames existing PVCs** — moves each PVC to `<name>-orig-<hash>` to
   preserve the current disk data as a safety net.
4. **Deletes DataVolumes** — removes DVs so CDI does not recreate PVCs before
   Velero restores them.
5. **Creates Velero Restore** — restores resources from the backup with:
   - `existingResourcePolicy: update` to overwrite existing objects.
   - Resource modifier rules that add `cdi.kubevirt.io/allowClaimAdoption=true`
     to restored PVCs so CDI can adopt them.
   - OVN IP/MAC annotations to preserve the VM's network identity.

After the Velero Restore completes, the HelmReleases resume and the VM boots
with the restored disk data while retaining its original IP and MAC addresses.

### Reclaiming the kept disks

With `keepOriginalPVC: true`, the default, step 3 switches each disk's PV to the `Retain` reclaim policy so that it outlives the PVC it is renamed from. Nothing removes the `-orig` PVCs or sets the policy back afterwards: they stay until an operator deletes them.

The policy the PV had before the restore is recorded in the `backups.cozystack.io/original-reclaim-policy` annotation, on the PV and on the `-orig` PVC. Set it back on the PV before deleting the `-orig` PVC, or a PV that was `Delete` stays `Retain` once released and its volume is never reclaimed. A PVC renamed by an older controller carries no annotation; the snippet then leaves the PV on `Retain` for you to decide.

```bash
PVC=vm-disk-ubuntu-source-orig-1a2b
PV=$(kubectl -n "$NAMESPACE" get pvc "$PVC" -o jsonpath='{.spec.volumeName}')
POLICY=$(kubectl get pv "$PV" -o jsonpath='{.metadata.annotations.backups\.cozystack\.io/original-reclaim-policy}')
kubectl patch pv "$PV" --type=merge -p "{\"spec\":{\"persistentVolumeReclaimPolicy\":\"${POLICY:-Retain}\"}}"
kubectl -n "$NAMESPACE" delete pvc "$PVC"
```

The `<hash>` is derived from the RestoreJob name, so an `-orig` PVC left behind by a deleted RestoreJob takes the name a new RestoreJob of the same name would use. That RestoreJob fails before it halts the VM, naming the PVC, rather than restoring without keeping the current disk. Reclaim the leftover PVC as above, or give the new RestoreJob another name.

## Method 2: Cross-Namespace Restore (Copy)

```bash
./07-restore-to-copy.sh
```

Creates a `RestoreJob` with `spec.options.targetNamespace` set to a different namespace
(default: `tenant-root-copy`). This creates an independent copy of the VM
without affecting the original.

Key differences from in-place restore:

| Aspect | In-Place | Cross-Namespace |
|---|---|---|
| Source VM affected | Yes (halted, PVCs renamed) | No (untouched) |
| Velero namespaceMapping | Not used | Maps source NS to target NS |
| OVN IP/MAC | Preserved from backup | Skipped (new IP/MAC assigned) |
| Pre-restore preparation | Full (suspend, halt, rename, delete) | Skipped |

The script ensures the target namespace exists before creating the RestoreJob.
Velero's `namespaceMapping` redirects all resources from the source namespace to
the target namespace during restore.

### Important limitation

Restoring to the **same namespace** with a **different application name** is not
supported. Velero's DataUpload always writes volume data to PVCs with the
original name regardless of resource modifiers, which causes conflicts. If you
attempt this, the RestoreJob will fail with an error message suggesting
cross-namespace restore instead.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `tenant-root` | Source namespace (where the Backup lives) |
| `TARGET_NAMESPACE` | `tenant-root-copy` | Target namespace for cross-namespace restore |

## Verify

After either restore method, verify the VM is running:

```bash
# In-place
kubectl get vmi -n tenant-root

# Cross-namespace copy
kubectl get vmi -n tenant-root-copy
```

## Cleanup

To remove all resources created by the demo:

```bash
./cleanup.sh
```

This deletes RestoreJobs, BackupJobs, Backups, VMInstance, VMDisk, BackupClass,
strategies, and the target namespace.
