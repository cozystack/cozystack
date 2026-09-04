#!/bin/bash
# Convenience runner + e2e harness for the standalone VMDisk backup/restore demo.
#
# Runs the numbered steps 01..05 a human reads, in order, non-interactively,
# stopping on the first failure (set -e). This is the SAME sequence the docs walk
# through, so the documented demo and the automated test can never drift.
#
# Flow: Velero strategy -> BackupClass -> a standalone VMDisk (waits imported) ->
# ad-hoc BackupJob (waits Succeeded) -> in-place RestoreJob (waits Succeeded, then
# waits for the restored disk to be Bound + Ready).
#
# Override NAMESPACE / BACKUP_STORAGE_LOCATION via the environment; see
# 00-helpers.sh. hack/e2e-chainsaw/vminstance/ drives this file as the gated
# vmdisk-2-backup-roundtrip test (VMI_E2E_S3_ROUNDTRIP=1).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "VMDisk Backup & Restore Demo"
log_info "Namespace: ${NAMESPACE}   BackupStorageLocation: ${BACKUP_STORAGE_LOCATION}"

SCRIPTS=(
    "01-create-strategy.sh"
    "02-create-backupclass.sh"
    "03-create-vmdisk.sh"
    "04-create-backupjob.sh"
    "05-restore-in-place.sh"
)

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
log_info "To clean up resources: ./cleanup.sh"
