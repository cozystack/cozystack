#!/bin/bash
# Script 02: Create tenants, one per scheduling class
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 2: Create Tenants"

# --- tenant colocate ---
log_step "Tenant: colocate (schedulingClass: colocate)"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: colocate
  namespace: tenant-root
spec:
  schedulingClass: colocate"

pause

kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: colocate
  namespace: tenant-root
spec:
  schedulingClass: colocate
EOF

log_success "Created tenant colocate"

separator

# --- tenant onepernode ---
log_step "Tenant: onepernode (schedulingClass: one-per-node)"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: onepernode
  namespace: tenant-root
spec:
  schedulingClass: one-per-node"

pause

kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: onepernode
  namespace: tenant-root
spec:
  schedulingClass: one-per-node
EOF

log_success "Created tenant onepernode"

separator

# --- tenant spread ---
log_step "Tenant: spread (schedulingClass: spread-evenly)"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: spread
  namespace: tenant-root
spec:
  schedulingClass: spread-evenly"

pause

kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: spread
  namespace: tenant-root
spec:
  schedulingClass: spread-evenly
EOF

log_success "Created tenant spread"

separator

# --- tenant node2 ---
log_step "Tenant: node2 (schedulingClass: pin-to-node2)"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: node2
  namespace: tenant-root
spec:
  schedulingClass: pin-to-node2"

pause

kubectl apply -f - <<'EOF'
apiVersion: apps.cozystack.io/v1alpha1
kind: Tenant
metadata:
  name: node2
  namespace: tenant-root
spec:
  schedulingClass: pin-to-node2
EOF

log_success "Created tenant node2"

separator
log_success "All tenants created."
log_command "kubectl get tenants -n tenant-root"
kubectl get tenants -n tenant-root >&2

echo -e "\n${GREEN}${BOLD}Next step:${NC} ./03-colocate-demo.sh"
