#!/bin/bash
# Script 11: Pin-to-node2 demo - all pods on a specific node
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-node2"
REDIS1="redis-demo"
REDIS2="redis-demo2"

print_header "Step 11: Pin-to-node2 Demo"

log_step "Creating first Redis in $NAMESPACE"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS1
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false"

pause

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS1
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false
EOF

log_success "Redis $REDIS1 created"

log_info "Waiting for pods to be scheduled..."
wait_for_redis_ready "$NAMESPACE" "$REDIS1"
wait_for_pods "$NAMESPACE"

log_step "All pods should be on node2:"
show_pods "$NAMESPACE"

pause

separator

log_step "Creating a second Redis in $NAMESPACE"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS2
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false"

pause

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS2
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false
EOF

log_success "Redis $REDIS2 created"

log_info "Waiting for pods to be scheduled..."
wait_for_redis_ready "$NAMESPACE" "$REDIS2"
wait_for_pods "$NAMESPACE"

log_step "Both Redis instances — all pods pinned to node2:"
show_pods "$NAMESPACE"

separator
log_success "Pin-to-node2 demo complete."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./12-node2-cleanup.sh"
