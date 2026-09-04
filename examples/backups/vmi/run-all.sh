#!/bin/bash
# Convenience runner + e2e harness for the VMInstance backup/restore demo.
#
# Runs the numbered steps 01..07 a human reads, in order, non-interactively,
# stopping on the first failure (set -e). This is the SAME sequence the docs
# walk through, so the documented flow and the automated test can never drift.
#
# Flow: Velero strategies -> BackupClass -> VMDisk + VMInstance (waits until the
# VM is booted) -> ad-hoc BackupJob (waits Succeeded) -> in-place RestoreJob
# (waits Succeeded, waits for the VM to boot again) -> cross-namespace to-copy
# RestoreJob (waits Succeeded).
#
# Override NAMESPACE / BACKUP_STORAGE_LOCATION / TARGET_NAMESPACE via the
# environment; see 00-helpers.sh. hack/e2e-chainsaw/vminstance/ drives this file
# as the gated vminstance-2-backup-roundtrip test (VMI_E2E_S3_ROUNDTRIP=1).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "VMInstance Backup & Restore Demo"
log_info "Namespace: ${NAMESPACE}   BackupStorageLocation: ${BACKUP_STORAGE_LOCATION}"

SCRIPTS=(
    "01-create-strategies.sh"
    "02-create-backupclass.sh"
    "03-create-vmdisk.sh"
    "04-create-vminstance.sh"
    "05-create-backupjob.sh"
    "06-restore-in-place.sh"
)
# Cross-namespace to-copy restore is the heaviest step (a second full VM in a
# fresh namespace). SKIP_RESTORE_TO_COPY=1 drops it — used by the lightweight CI
# round-trip, which proves backup + in-place restore is enough.
if [[ "${SKIP_RESTORE_TO_COPY:-0}" != "1" ]]; then
    SCRIPTS+=("07-restore-to-copy.sh")
fi

for script in "${SCRIPTS[@]}"; do
    if [[ -x "$SCRIPT_DIR/$script" ]]; then
        "$SCRIPT_DIR/$script"
        separator
    else
        log_error "Script not found or not executable: $script"
        exit 1
    fi
done

print_header "Demo Complete"

log_success "All steps completed successfully!"
log_info "What was demonstrated:"
echo "  1. Created Velero backup strategies for VMInstance and VMDisk" >&2
echo "  2. Created BackupClass binding strategies to application types" >&2
echo "  3. Provisioned a VMDisk and VMInstance" >&2
echo "  4. Created a backup of the VMInstance via BackupJob" >&2
echo "  5. Restored the VMInstance in-place" >&2
echo "  6. Restored the VMInstance to a copy in a different namespace" >&2
log_info "To clean up resources: ./cleanup.sh"
