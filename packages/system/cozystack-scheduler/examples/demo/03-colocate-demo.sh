#!/bin/bash
# Script 03: Colocate demo - all pods of a Redis land on the same node
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

NAMESPACE="tenant-colocate"
REDIS_NAME="redis-demo"

print_header "Step 3: Colocate Demo"

log_step "Creating Redis in $NAMESPACE (1 replica + 3 sentinels)"

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

separator

log_step "Waiting for pods to be scheduled..."
wait_for_redis_ready "$NAMESPACE" "$REDIS_NAME"
wait_for_pods "$NAMESPACE"

log_step "All pods should be on the SAME node:"
show_pods "$NAMESPACE"

pause

separator

log_step "Scaling up replicas to 9..."
log_command "kubectl patch redis $REDIS_NAME -n $NAMESPACE --type=merge -p '{\"spec\":{\"replicas\":9}}'"

kubectl patch redis "$REDIS_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"replicas":9}}'

log_info "Waiting for new pods..."
sleep 15

log_step "All pods (storage + sentinels) are still on the same node:"
show_pods "$NAMESPACE"

separator
log_success "Colocate demo complete. All pods landed on one node."
echo -e "\n${GREEN}${BOLD}Next step:${NC} ./04-colocate-second-redis.sh"
