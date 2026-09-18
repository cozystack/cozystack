#!/bin/bash
# Step 06: Restore the VMInstance in-place (same namespace, same application)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 6: Restore VMInstance In-Place"

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

# VirtualMachine Ready (and status.interfaces[].ipAddress) is the virt-launcher
# pod's readiness/IP, reported before the guest OS runs, so it cannot by itself
# tell a restored disk from a blank one. A guest-produced signal is not available
# on this platform: serial-console logging is disabled cluster-wide (the KubeVirt
# CR sets disableSerialConsoleLog) and the cirros CI guest ships no qemu-guest-
# agent. So prove provenance the way the VMDisk round-trip does. keepOriginalPVC
# renamed the original disk out of the way, so a Bound vm-disk-ubuntu-source that
# carries Velero's restore-name label is one the restore recreated from the
# backup — and the VM above is Running off it, not off the original.
log_step "Verifying the VM booted off a disk the restore recreated from the backup..."
wait_for_field pvc vm-disk-ubuntu-source '{.status.phase}' Bound "$NAMESPACE" 300
restore_label=$(kubectl -n "$NAMESPACE" get pvc vm-disk-ubuntu-source \
    -o "jsonpath={.metadata.labels.velero\.io/restore-name}")
[[ -n "$restore_label" ]] || {
    log_error "restored VM disk PVC carries no velero.io/restore-name label — it was not recreated from the backup"
    exit 1
}

separator

log_success "In-place restore completed; VM disk recreated from backup (${restore_label}) and the VM is Running off it"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./07-restore-to-copy.sh"
