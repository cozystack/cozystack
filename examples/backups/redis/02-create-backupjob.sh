#!/bin/bash
# Step 02: Create a BackupJob against the platform cozy-default BackupClass. The
# controller resolves it to the cozy-default-redis strategy, projects
# cozy-backups-creds into the namespace, renders the Pod template against the
# Redis app, and runs it as a batch/v1.Job that dumps an RDB snapshot to S3.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 02: Create BackupJob '${BACKUPJOB_NAME}'"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${BACKUPJOB_NAME}
  namespace: ${NAMESPACE}
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: Redis
    name: ${REDIS_NAME}
  backupClassName: ${BACKUPCLASS_NAME}
EOF

log_substep "Waiting for BackupJob to Succeed (dumps the RDB and uploads to S3)..."
# wait_for_field polls to the terminal phase directly: a fast strategy Pod can
# go Pending -> Succeeded between two observations, so an explicit "wait Running"
# gate would flake.
wait_for_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 600

backup_ref=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')
[[ -n "$backup_ref" ]] || { log_error "BackupJob succeeded but BackupRef is empty"; exit 1; }
log_success "Backup '${backup_ref}' is Ready."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-restore-in-place.sh"
