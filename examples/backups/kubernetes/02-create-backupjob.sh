#!/bin/bash
# Step 02: Back the cluster up through the cozy-default BackupClass. The
# controller dumps the cluster's etcd prefix to the platform bucket with a
# Job, then backs up its control-plane Secrets and volumes with Velero.
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
    kind: Kubernetes
    name: ${CLUSTER_NAME}
  backupClassName: ${BACKUPCLASS_NAME}
EOF

wait_for_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded "$NAMESPACE" 1800 Failed

warn=$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.conditions[?(@.type=="CertificatesExpiring")].status}')
if [[ "$warn" == "True" ]]; then
    log_warning "$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" \
        -o jsonpath='{.status.conditions[?(@.type=="CertificatesExpiring")].message}')"
fi
log_success "Backup '$(kubectl -n "$NAMESPACE" get backupjob "$BACKUPJOB_NAME" -o jsonpath='{.status.backupRef.name}')' is Ready."

echo -e "\n${GREEN}${BOLD}Next:${NC} ./03-restore-in-place.sh"
