#!/bin/bash
# Step 03: Apply the source Etcd application, wait for the operator-side
# EtcdCluster to become Available, then write a sentinel key that the backup
# captures and the restore round-trips. The sentinel is the in-cluster
# witness that the snapshot actually round-tripped through S3.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 03: Provision source Etcd '${ETCD_NAME}' and write sentinel"

# The chart pins the release name to "etcd"; ETCD_NAME must equal "etcd".
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: ${ETCD_NAME}
  namespace: ${NAMESPACE}
spec:
  size: 1Gi
  replicas: 3
  resources:
    cpu: 200m
    memory: 256Mi
EOF

log_substep "Waiting for HelmRelease 'etcd' to be Ready..."
kubectl -n "$NAMESPACE" wait hr "${ETCD_NAME}" --for=condition=ready --timeout=300s
log_substep "Waiting for etcd-operator.cozystack.io/EtcdCluster 'etcd' to be Available..."
kubectl -n "$NAMESPACE" wait etcdcluster.etcd-operator.cozystack.io etcd \
    --for=jsonpath='{.status.conditions[?(@.type=="Available")].status}'=True \
    --timeout=300s

log_substep "Waiting for etcd member pods to be Ready..."
kubectl -n "$NAMESPACE" wait pod -l app.kubernetes.io/instance=etcd --for=condition=ready --timeout=300s

SENTINEL_KEY="__cozystack_e2e_sentinel"
SENTINEL_VAL="pristine-$(date -u +%Y%m%dT%H%M%SZ)"
log_substep "Writing sentinel key ${SENTINEL_KEY}=${SENTINEL_VAL}..."
etcdctl_exec put "$SENTINEL_KEY" "$SENTINEL_VAL"
GOT=$(etcdctl_exec get "$SENTINEL_KEY" --print-value-only)
[[ "$GOT" == "$SENTINEL_VAL" ]] || { log_error "sentinel readback mismatch: got '$GOT'"; exit 1; }

umask 077
echo "export SENTINEL_KEY=${SENTINEL_KEY}" >  "$SCRIPT_DIR/.sentinel.env"
echo "export SENTINEL_VAL=${SENTINEL_VAL}" >> "$SCRIPT_DIR/.sentinel.env"
chmod 600 "$SCRIPT_DIR/.sentinel.env"

log_success "Etcd '${ETCD_NAME}' Ready; sentinel written and cached in $(basename "$SCRIPT_DIR")/.sentinel.env."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./04-create-backupjob.sh"
