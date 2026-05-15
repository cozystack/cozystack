#!/bin/bash
# Script 09: Spread-evenly demo - topology spread constraints
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-spread"
REDIS_NAME="redis-demo"

print_header "Step 9: Spread-evenly Demo"

log_step "Creating Redis in $NAMESPACE (1 replica)"

show_manifest "apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $REDIS_NAME
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
  name: $REDIS_NAME
  namespace: $NAMESPACE
spec:
  replicas: 1
  storageClass: local
  resourcesPreset: nano
  authEnabled: false
EOF

log_success "Redis $REDIS_NAME created"

log_info "Waiting for pods to be scheduled..."
wait_for_redis_ready "$NAMESPACE" "$REDIS_NAME"
wait_for_pods "$NAMESPACE"

log_step "Initial pod distribution (1 storage + 3 sentinels):"
show_pods "$NAMESPACE"

pause

separator

log_step "Scaling up to 9 replicas..."
log_command "kubectl patch redis $REDIS_NAME -n $NAMESPACE --type=merge -p '{\"spec\":{\"replicas\":9}}'"

kubectl patch redis "$REDIS_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"replicas":9}}'

log_info "Waiting for new pods..."
sleep 20

log_step "Pods should be evenly distributed across all nodes (maxSkew=1):"
show_pods "$NAMESPACE"

separator

log_step "Pod count per node:"
kubectl get pods -n "$NAMESPACE" -o wide --no-headers 2>/dev/null \
    | awk '{print $7}' | sort | uniq -c | sort -rn >&2

separator
log_success "Spread-evenly demo complete. Pods are balanced across nodes."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./10-spread-cleanup.sh"
