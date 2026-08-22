#!/bin/bash
# Script 01: Create SchedulingClasses
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 1: Create SchedulingClasses"

# --- pin-to-node2 ---
log_step "SchedulingClass: pin-to-node2"
log_info "All pods will always be scheduled to node2."

show_manifest "apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: pin-to-node2
spec:
  nodeSelector:
    kubernetes.io/hostname: node2"

pause

kubectl apply -f - <<'EOF'
apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: pin-to-node2
spec:
  nodeSelector:
    kubernetes.io/hostname: node2
EOF

log_success "Created SchedulingClass pin-to-node2"

separator

# --- one-per-node ---
log_step "SchedulingClass: one-per-node"
log_info "Pods of the same application will never share a node."

show_manifest "apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: one-per-node
spec:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
    - topologyKey: kubernetes.io/hostname"

pause

kubectl apply -f - <<'EOF'
apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: one-per-node
spec:
  podAntiAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
    - topologyKey: kubernetes.io/hostname
EOF

log_success "Created SchedulingClass one-per-node"

separator

# --- spread-evenly ---
log_step "SchedulingClass: spread-evenly"
log_info "Pods will be spread evenly across nodes (maxSkew=1)."

show_manifest "apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: spread-evenly
spec:
  topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: DoNotSchedule"

pause

kubectl apply -f - <<'EOF'
apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: spread-evenly
spec:
  topologySpreadConstraints:
  - maxSkew: 1
    topologyKey: kubernetes.io/hostname
    whenUnsatisfiable: DoNotSchedule
EOF

log_success "Created SchedulingClass spread-evenly"

separator

# --- colocate ---
log_step "SchedulingClass: colocate"
log_info "All pods of the same application will land on the same node."

show_manifest "apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: colocate
spec:
  podAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
    - topologyKey: kubernetes.io/hostname"

pause

kubectl apply -f - <<'EOF'
apiVersion: cozystack.io/v1alpha1
kind: SchedulingClass
metadata:
  name: colocate
spec:
  podAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
    - topologyKey: kubernetes.io/hostname
EOF

log_success "Created SchedulingClass colocate"

separator
log_success "All SchedulingClasses created."
log_command "kubectl get schedulingclasses"
kubectl get schedulingclasses >&2

echo -e "\n${GREEN}${BOLD}Next step:${NC} ./02-create-tenants.sh"
