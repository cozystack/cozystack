#!/bin/bash
# Step 05: Restore the standalone VMDisk in-place from its backup.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 5: Restore VMDisk In-Place"

PVC=vm-disk-backup-src
CONSUMER=vm-disk-backup-src-restore-consumer

orig_uid=$(kubectl -n "$NAMESPACE" get pvc "$PVC" -o jsonpath='{.metadata.uid}')
[[ -n "$orig_uid" ]] || { log_error "PVC $PVC has no uid before the restore"; exit 1; }
log_info "PVC $PVC before restore: uid $orig_uid"

log_step "Creating RestoreJob 'vmdisk-restore-in-place' in namespace $NAMESPACE..."
log_info "In-place restore: the original PVC is renamed and the disk is restored from backup"
log_command "kubectl apply -f - (RestoreJob: vmdisk-restore-in-place)"

# The controller suspends the vm-disk-<name> HelmRelease, deletes its DataVolume
# and (keepOriginalPVC) renames the live PVC to <name>-orig-<hash>; Velero then
# creates a fresh PVC under the original name for the data mover to populate.
kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: vmdisk-restore-in-place
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: vmdisk-backup
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: VMDisk
    name: backup-src
  options:
    keepOriginalPVC: true # rename the original PVC to <name>-orig-<hash> before restoring
EOF

log_success "RestoreJob created"

# On a WaitForFirstConsumer StorageClass the Velero data mover does not start
# until a scheduled Pod has picked a node for the restored PVC (Velero's
# WaitPVCConsumed). A VMInstance restore gets that from the virt-launcher Pod; a
# detached disk has no consumer, so stand in for the VM that would attach it.
# Only once the new PVC exists: a Pod mounting the original would pin it with
# pvc-protection and block the rename.
start_consumer() {
    local mode volume mount
    mode=$(kubectl -n "$NAMESPACE" get pvc "$PVC" -o jsonpath='{.spec.volumeMode}')
    if [[ "$mode" == "Block" ]]; then
        mount='volumeDevices: [{name: disk, devicePath: /dev/disk0}]'
    else
        mount='volumeMounts: [{name: disk, mountPath: /disk}]'
    fi
    volume="{name: disk, persistentVolumeClaim: {claimName: $PVC}}"
    log_info "Starting consumer Pod $CONSUMER for the restored PVC ($mode)"
    kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${CONSUMER}
  namespace: ${NAMESPACE}
spec:
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: pause
    image: registry.k8s.io/pause:3.9
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
    ${mount}
  volumes:
  - ${volume}
EOF
}

log_step "Waiting for RestoreJob to complete..."
log_command "waiting for restorejob/vmdisk-restore-in-place {.status.phase} == 'Succeeded'"
consumer_started=false
elapsed=0
timeout="${RESTORE_WAIT:-1800}"
while true; do
    phase=$(kubectl -n "$NAMESPACE" get restorejob vmdisk-restore-in-place -o jsonpath='{.status.phase}' || true)
    [[ "$phase" == "Succeeded" ]] && break
    if [[ "$phase" == "Failed" ]]; then
        log_error "restorejob/vmdisk-restore-in-place reached terminal failure state 'Failed'"
        exit 1
    fi
    if [[ "$consumer_started" == false ]]; then
        # A PVC CDI recreated in the rename window has a new uid too but no
        # restore label; mounting it would pin it while the controller clears it.
        read -r uid restored < <(kubectl -n "$NAMESPACE" get pvc "$PVC" --ignore-not-found \
            -o jsonpath='{.metadata.uid} {.metadata.labels.velero\.io/restore-name}' || true) || true
        if [[ -n "${uid:-}" && "$uid" != "$orig_uid" && -n "${restored:-}" ]]; then
            start_consumer
            consumer_started=true
        fi
    fi
    if [[ $elapsed -ge $timeout ]]; then
        log_error "Timeout waiting for restorejob/vmdisk-restore-in-place (current: '$phase', expected: 'Succeeded', consumer started: $consumer_started)"
        exit 1
    fi
    sleep 5
    elapsed=$((elapsed + 5))
    echo -n "." >&2
done
log_success "restorejob/vmdisk-restore-in-place reached 'Succeeded'"

kubectl -n "$NAMESPACE" delete pod "$CONSUMER" --ignore-not-found --timeout=120s

# Neither the restore-name label nor a new uid proves the data moved: Velero's
# existingResourcePolicy: update stamps the label on a PVC it only patched, and
# CDI recreating the disk would also give it a new uid. The DataDownload check
# below does; the uid and the -orig- PVC show the original was kept aside.
log_step "Verifying the PVC was recreated from the backup..."
wait_for_field pvc "$PVC" '{.status.phase}' Bound "$NAMESPACE" 120
new_uid=$(kubectl -n "$NAMESPACE" get pvc "$PVC" -o jsonpath='{.metadata.uid}')
[[ "$new_uid" != "$orig_uid" ]] || {
    log_error "PVC $PVC still has its pre-restore uid $orig_uid — the restore did not recreate it"
    exit 1
}
restore_label=$(kubectl -n "$NAMESPACE" get pvc "$PVC" \
    -o "jsonpath={.metadata.labels.velero\.io/restore-name}")
[[ -n "$restore_label" ]] || {
    log_error "PVC $PVC carries no velero.io/restore-name label — Velero did not restore it"
    exit 1
}
all_pvcs=$(kubectl -n "$NAMESPACE" get pvc -o name)
[[ "$all_pvcs" == *"persistentvolumeclaim/${PVC}-orig-"* ]] || {
    log_error "no ${PVC}-orig-* PVC — keepOriginalPVC did not rename the original disk"
    exit 1
}
# The DataDownload carries the same restore-name label as the PVC; one that
# Completed having written bytes is what shows the data mover filled the disk.
downloads=$(kubectl -n cozy-velero get datadownloads.velero.io -l "velero.io/restore-name=${restore_label}" \
    -o jsonpath='{range .items[*]}{.status.phase} {.status.progress.bytesDone}{"\n"}{end}')
grep -Eq '^Completed [1-9][0-9]*$' <<<"$downloads" || {
    log_error "no Completed DataDownload with data for restore ${restore_label} — the data mover did not restore $PVC (got: ${downloads:-none})"
    exit 1
}

log_step "Waiting for the restored VMDisk to become Ready again..."
# The HelmRelease was Ready before the controller suspended it, and that
# condition outlives the restore; only once Flux has reconciled the restored
# spec does Ready describe the restored release.
hr_generation=$(kubectl -n "$NAMESPACE" get hr vm-disk-backup-src -o jsonpath='{.metadata.generation}')
wait_for_field hr vm-disk-backup-src '{.status.observedGeneration}' "$hr_generation" "$NAMESPACE" 120
wait_hr_ready vm-disk-backup-src 180

kubectl get restorejob vmdisk-restore-in-place -n "$NAMESPACE" -o wide >&2 || true
log_success "Restore completed; VMDisk 'backup-src' recreated from backup (${restore_label}, uid ${new_uid}), Bound and Ready"
