#!/bin/bash
# Clean up all resources created by the demo
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

TARGET_NAMESPACE="${TARGET_NAMESPACE:-tenant-root-copy}"

print_header "Cleanup Demo Resources"

log_warning "This script will delete all resources created during the demo"
echo -e "\n${YELLOW}The following will be deleted:${NC}"
echo "  - RestoreJobs (restore-in-place-test, restore-to-copy-test)"
echo "  - BackupJob (test-backup) and associated Backup"
echo "  - VMInstance (test)"
echo "  - VMDisk (ubuntu-source)"
echo "  - BackupClass (velero)"
echo "  - Velero strategies (vminstance-strategy, vmdisk-strategy)"
echo "  - Target namespace ($TARGET_NAMESPACE)"
echo ""

read -p "Continue? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    log_info "Cancelled"
    exit 0
fi

separator

log_step "Deleting RestoreJobs..."
kubectl delete restorejob restore-to-copy-test -n "$NAMESPACE" 2>/dev/null || log_warning "RestoreJob restore-to-copy-test not found"
kubectl delete restorejob restore-in-place-test -n "$NAMESPACE" 2>/dev/null || log_warning "RestoreJob restore-in-place-test not found"

separator

log_step "Deleting BackupJob and Backup..."
kubectl delete backupjob test-backup -n "$NAMESPACE" 2>/dev/null || log_warning "BackupJob test-backup not found"
kubectl delete backup test-backup -n "$NAMESPACE" 2>/dev/null || log_warning "Backup test-backup not found"

separator

log_step "Deleting VMInstance..."
kubectl delete vminstance test -n "$NAMESPACE" 2>/dev/null || log_warning "VMInstance test not found"

log_step "Deleting VMDisk..."
kubectl delete vmdisk ubuntu-source -n "$NAMESPACE" 2>/dev/null || log_warning "VMDisk ubuntu-source not found"

separator

log_step "Deleting BackupClass..."
kubectl delete backupclass velero 2>/dev/null || log_warning "BackupClass velero not found"

separator

log_step "Deleting Velero strategies..."
kubectl delete velero.strategy.backups.cozystack.io vminstance-strategy 2>/dev/null || log_warning "Strategy vminstance-strategy not found"
kubectl delete velero.strategy.backups.cozystack.io vmdisk-strategy 2>/dev/null || log_warning "Strategy vmdisk-strategy not found"

separator

log_step "Deleting target namespace..."
kubectl delete namespace "$TARGET_NAMESPACE" 2>/dev/null || log_warning "Namespace $TARGET_NAMESPACE not found"

separator

log_success "Cleanup complete"
log_info "All demo resources have been deleted"
echo -e "\n${GREEN}${BOLD}To re-run the demo:${NC} ./01-create-strategies.sh"
