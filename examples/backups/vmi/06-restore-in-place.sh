#!/bin/bash
# Step 06: Restore the VMInstance in-place (same namespace, same application)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 6: Restore VMInstance In-Place"

orig_uid=$(kubectl -n "$NAMESPACE" get pvc vm-disk-ubuntu-source -o jsonpath='{.metadata.uid}')
[[ -n "$orig_uid" ]] || { log_error "PVC vm-disk-ubuntu-source has no uid before the restore"; exit 1; }

log_step "Creating RestoreJob 'restore-in-place-test' in namespace $NAMESPACE..."
log_info "In-place restore: the VM will be halted, PVCs renamed, and data restored from backup"
log_command "kubectl apply -f - (RestoreJob: restore-in-place-test)"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: restore-in-place-test
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: test-backup
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: VMInstance
    name: test
  options:
    failIfTargetExists: true # only enforced for restore-to-copy; a no-op for in-place, whose source is expected to exist
    keepOriginalPVC: true # renames original VMI PVC before restore to <name>-orig-<hash>, only for in-place restore
    keepOriginalIpAndMac: true # restores original IP and MAC address of VMI via OVN annotations
EOF

log_success "RestoreJob created"

separator

log_step "Waiting for RestoreJob to complete..."
wait_for_field restorejob restore-in-place-test '{.status.phase}' Succeeded "$NAMESPACE" "${RESTORE_WAIT:-1800}" Failed

separator

log_step "Verifying RestoreJob result..."
log_command "kubectl get restorejob restore-in-place-test -n $NAMESPACE -o yaml"
kubectl get restorejob restore-in-place-test -n "$NAMESPACE" -o wide

separator

# The RestoreJob reaching Succeeded means Velero re-created the objects; prove
# the restored VM actually boots from the restored disk (the round-trip proof
# for a VM, which has no in-guest sentinel we can read from the host).
log_step "Waiting for the restored VMInstance to boot again..."
log_command "kubectl -n $NAMESPACE wait virtualmachine.kubevirt.io/vm-instance-test --for=condition=Ready"
kubectl -n "$NAMESPACE" wait virtualmachine.kubevirt.io/vm-instance-test \
    --for=condition=Ready --timeout="${VMI_BOOT_WAIT:-600}s"

# VirtualMachine Ready is the virt-launcher Pod's readiness, reported before the
# guest runs, so it cannot tell a restored disk from a blank one, and this
# platform offers no guest-side signal (serial-console logging is disabled
# cluster-wide, the cirros guest has no qemu-guest-agent). Prove provenance on the
# PVC instead. Velero's existingResourcePolicy: update stamps
# velero.io/restore-name even on a PVC it only patched, so also require a new uid
# (Velero created the PVC, the only path that runs the data mover) and the
# -orig- PVC that keepOriginalPVC leaves behind.
log_step "Verifying the VM booted off a disk the restore recreated from the backup..."
wait_for_field pvc vm-disk-ubuntu-source '{.status.phase}' Bound "$NAMESPACE" 120
new_uid=$(kubectl -n "$NAMESPACE" get pvc vm-disk-ubuntu-source -o jsonpath='{.metadata.uid}')
[[ "$new_uid" != "$orig_uid" ]] || {
    log_error "PVC vm-disk-ubuntu-source still has its pre-restore uid $orig_uid — the restore did not recreate it"
    exit 1
}
restore_label=$(kubectl -n "$NAMESPACE" get pvc vm-disk-ubuntu-source \
    -o "jsonpath={.metadata.labels.velero\.io/restore-name}")
[[ -n "$restore_label" ]] || {
    log_error "restored VM disk PVC carries no velero.io/restore-name label — Velero did not restore it"
    exit 1
}
all_pvcs=$(kubectl -n "$NAMESPACE" get pvc -o name)
[[ "$all_pvcs" == *"persistentvolumeclaim/vm-disk-ubuntu-source-orig-"* ]] || {
    log_error "no vm-disk-ubuntu-source-orig-* PVC — keepOriginalPVC did not rename the original disk"
    exit 1
}

separator

log_success "In-place restore completed; VM disk recreated from backup (${restore_label}, uid ${new_uid}) and the VM is Running off it"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./07-restore-to-copy.sh"
