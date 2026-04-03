#!/bin/bash
# Run the full VMInstance backup/restore demo sequentially
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "VMInstance Backup & Restore Demo"

log_info "This script will run all demo steps sequentially"
log_info "There will be a pause between steps for observation"
echo ""

SCRIPTS=(
    "01-create-strategies.sh"
    "02-create-backupclass.sh"
    "03-create-vmdisk.sh"
    "04-create-vminstance.sh"
    "05-create-backupjob.sh"
    "06-restore-in-place.sh"
    "07-restore-to-copy.sh"
)

echo -e "${CYAN}Demo steps:${NC}"
for i in "${!SCRIPTS[@]}"; do
    echo "  $((i+1)). ${SCRIPTS[$i]}"
done
echo ""

read -p "Run demo? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    log_info "Cancelled"
    exit 0
fi

separator

for script in "${SCRIPTS[@]}"; do
    if [[ -x "$SCRIPT_DIR/$script" ]]; then
        "$SCRIPT_DIR/$script"
        separator
        wait_for_enter
    else
        log_error "Script not found or not executable: $script"
        exit 1
    fi
done

print_header "Demo Complete"

log_success "All steps completed successfully!"
echo ""
log_info "What was demonstrated:"
echo "  1. Created Velero backup strategies for VMInstance and VMDisk"
echo "  2. Created BackupClass binding strategies to application types"
echo "  3. Provisioned a VMDisk and VMInstance"
echo "  4. Created a backup of the VMInstance via BackupJob"
echo "  5. Restored the VMInstance in-place"
echo "  6. Restored the VMInstance to a copy in a different namespace"
echo ""
log_info "To clean up resources: ./cleanup.sh"
