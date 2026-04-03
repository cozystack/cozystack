#!/bin/bash
# Step 03: Create a VMDisk with Ubuntu cloud image
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 3: Create VMDisk"

log_step "Creating VMDisk 'ubuntu-source' in namespace $NAMESPACE..."
log_info "This will download the Ubuntu Noble cloud image (~700MB)"
log_command "kubectl apply -f - (VMDisk: ubuntu-source)"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: ubuntu-source
  namespace: ${NAMESPACE}
spec:
  optical: false
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  storage: 20Gi
  storageClass: replicated
EOF

log_success "VMDisk created"

separator

log_step "Verifying VMDisk..."
log_command "kubectl get vmdisk ubuntu-source -n $NAMESPACE"
kubectl get vmdisk ubuntu-source -n "$NAMESPACE"

separator

log_success "VMDisk is ready (image download may still be in progress)"
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./04-create-vminstance.sh"
