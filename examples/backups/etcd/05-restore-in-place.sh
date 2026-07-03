#!/bin/bash
# Step 05: Demonstrate destructive in-place restore. Overwrites the
# sentinel value, submits a RestoreJob, and verifies that after the
# driver finishes the round-trip (suspend HR -> snapshot live spec ->
# delete EtcdCluster -> recreate with bootstrap.restore -> resume HR)
# the sentinel reads back as its pre-mutation value.
#
# To-copy is intentionally not demonstrated: the Etcd chart pins the
# Helm release name to "etcd" (templates/check-release-name.yaml), so
# two Etcd applications cannot coexist in one namespace, and
# RestoreJob.spec.targetApplicationRef is a same-namespace reference -
# the driver rejects any non-empty TargetApplicationRef with a
# phase=Failed explaining the limitation.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"
[[ -f "$SCRIPT_DIR/.sentinel.env"    ]] && source "$SCRIPT_DIR/.sentinel.env"
[[ -f "$SCRIPT_DIR/.backup-name.env" ]] && source "$SCRIPT_DIR/.backup-name.env"

: "${SENTINEL_KEY:?run 03-create-etcd-src.sh first}"
: "${SENTINEL_VAL:?run 03-create-etcd-src.sh first}"
: "${BACKUP_NAME:?run 04-create-backupjob.sh first}"

print_header "Step 05: In-place restore of '${ETCD_NAME}' from Backup '${BACKUP_NAME}'"

log_substep "Mutating sentinel before restore..."
MUTATED="mutated-$(date -u +%Y%m%dT%H%M%SZ)"
etcdctl_exec put "$SENTINEL_KEY" "$MUTATED"
PRE=$(etcdctl_exec get "$SENTINEL_KEY" --print-value-only)
[[ "$PRE" == "$MUTATED" ]] || { log_error "sentinel mutate readback mismatch"; exit 1; }
log_substep "Sentinel before restore: ${PRE}"

kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: ${RESTOREJOB_INPLACE_NAME}
  namespace: ${NAMESPACE}
spec:
  backupRef:
    name: ${BACKUP_NAME}
EOF

wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 1800 Failed

log_substep "Waiting for etcd member pods to be Ready again after restore..."
kubectl -n "$NAMESPACE" wait pod -l app.kubernetes.io/instance=etcd --for=condition=ready --timeout=300s

# Helm's next apply re-renders the chart's bootstrap-less EtcdCluster
# spec, which evicts the bootstrap.restore block the driver stamped on
# during the destructive window. Verify the field is gone before
# declaring the round-trip done; if Helm's reconcile lags here, a
# subsequent unrelated HR sync would otherwise produce a diff war
# (Helm: no bootstrap, operator: bootstrap.restore still present) that
# spuriously reboots the cluster.
log_substep "Waiting for Helm to evict bootstrap.restore from the live EtcdCluster..."
elapsed=0
while true; do
    BOOTSTRAP=$(kubectl -n "$NAMESPACE" get etcdcluster.etcd-operator.cozystack.io etcd \
        -o jsonpath='{.spec.bootstrap}' 2>/dev/null || true)
    if [[ -z "$BOOTSTRAP" || "$BOOTSTRAP" == "{}" || "$BOOTSTRAP" == "null" ]]; then
        log_success "bootstrap.restore evicted by Helm reconcile"
        break
    fi
    if [[ $elapsed -ge 300 ]]; then
        log_warning "bootstrap.restore still present after 300s; chart reconcile may be lagging (current: ${BOOTSTRAP})"
        break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
done

POST=$(etcdctl_exec get "$SENTINEL_KEY" --print-value-only)
log_substep "Sentinel after restore: ${POST}"
if [[ "$POST" != "$SENTINEL_VAL" ]]; then
    log_error "restore round-trip FAILED: expected ${SENTINEL_VAL}, got ${POST}"
    exit 1
fi
log_success "Restore round-trip OK; sentinel reverted to pre-mutation value."
echo -e "\n${GREEN}${BOLD}Next:${NC} ./cleanup.sh   ${WHITE}# optional teardown${NC}"
