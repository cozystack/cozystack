#!/bin/bash
# Script 06: One-per-node demo - anti-affinity prevents pods on the same node
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-onepernode"
REDIS_NAME="redis-demo"

print_header "Step 6: One-per-node Demo"

log_step "Creating Redis in $NAMESPACE (1 replica)"
log_info "We'll also reduce sentinels to 2 (1 storage + 2 sentinels = 3 pods for 3 nodes)."

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

log_info "Waiting for RedisFailover to appear..."
sleep 10

log_step "Patching RedisFailover to use 2 sentinels instead of 3"
log_info "With 3 nodes and anti-affinity, 1 storage + 3 sentinels = 4 pods won't fit."
log_command "kubectl patch rf $REDIS_NAME -n $NAMESPACE --type=merge -p '{\"spec\":{\"sentinel\":{\"replicas\":2}}}'"

kubectl patch rf redis-"$REDIS_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"sentinel":{"replicas":2}}}'

log_info "Waiting for pods to be scheduled..."
wait_for_pods "$NAMESPACE"
sleep 5

log_step "Each pod is on a different node (1 storage + 2 sentinels on 3 nodes):"
show_pods "$NAMESPACE"

pause

separator

log_step "Scaling up to 3 replicas..."
log_command "kubectl patch redis $REDIS_NAME -n $NAMESPACE --type=merge -p '{\"spec\":{\"replicas\":3}}'"

kubectl patch redis "$REDIS_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"replicas":3}}'

log_info "Waiting for new pods..."
sleep 15

log_step "Some pods should now be Pending (anti-affinity prevents scheduling):"
show_pods "$NAMESPACE"

pause

separator

log_step "Describing a Pending pod to see why it can't be scheduled..."
PENDING_POD=$(kubectl get pods -n "$NAMESPACE" --field-selector=status.phase=Pending -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")

if [[ -n "$PENDING_POD" ]]; then
    log_command "kubectl describe pod $PENDING_POD -n $NAMESPACE | tail -10"
    kubectl describe pod "$PENDING_POD" -n "$NAMESPACE" 2>&1 | tail -10 >&2
else
    log_warning "No Pending pods found (pods may still be initializing)."
    log_command "kubectl get pods -n $NAMESPACE -o wide"
fi

separator
log_success "One-per-node demo complete. Anti-affinity prevents co-scheduling."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./07-onepernode-second-redis.sh"
