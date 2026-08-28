#!/bin/bash
# End-to-end RabbitMQ definitions backup/restore demo against the platform
# cozy-default flow. It proves DATA INTEGRITY, not liveness: a sentinel vhost
# (a broker definition) is seeded, backed up, dropped, and must reappear after a
# restore — first in place, then into a separate copy.
#
# Requires the platform default backups stack: the backupstrategy-controller,
# the cozy-backups bucket, and the cozy-default-rabbitmq strategy it ships.
# No demo Bucket is created — the strategy carries the system-bucket coordinates
# and the controller projects cozy-backups-creds before each run.
#
# Env knobs: NAMESPACE (default tenant-root), SKIP_RESTORE=1 to stop after a
# successful backup (backup-only smoke).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "RabbitMQ definitions backup/restore demo (namespace: $NAMESPACE)"

# --- Precondition: the platform default backups stack is present -------------
log_step "Checking the cozy-default-rabbitmq strategy is installed"
# The Strategy CR is gated on a resolved bucket name, so its presence also
# confirms the cozy-backups bucket has reconciled. Fail fast with a pointed
# message rather than letting the BackupJob hang if backups are not enabled.
if ! kubectl get rabbitmqs.strategy.backups.cozystack.io cozy-default-rabbitmq >/dev/null 2>&1; then
    log_error "cozy-default-rabbitmq strategy not found: enable the platform default backups (backupstrategy-controller + cozy-backups bucket) before running this demo"
    exit 1
fi
log_success "cozy-default-rabbitmq strategy present"

# --- Source application + sentinel -------------------------------------------
log_step "Provisioning source RabbitMQ '$RABBITMQ_SRC_NAME'"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/05-rabbitmq-src.yaml"
wait_hr_ready "$RABBITMQ_SRC_CR" 300
wait_rabbitmq_ready "$RABBITMQ_SRC_CR" 600

SENTINEL="sentinel-$(date +%s)-$$"
log_step "Seeding sentinel definition (vhost + policy): $SENTINEL"
rabbitmq_seed_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL"
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "sentinel vhost did not seed on the source"; exit 1; }
log_success "sentinel present on the source"

# --- Backup ------------------------------------------------------------------
log_step "Creating ad-hoc BackupJob (backupClassName=$BACKUPCLASS_NAME)"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/10-backupjob-adhoc.yaml"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/15-plan.yaml"
wait_for_field backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed

BACKUP_NAME=$(kubectl -n "$NAMESPACE" get backupjobs.backups.cozystack.io "$BACKUPJOB_NAME" \
    -o jsonpath='{.status.backupRef.name}')
[[ -n "$BACKUP_NAME" ]] || { log_error "BackupJob succeeded but reported no backupRef"; exit 1; }
ARTIFACT_URI=$(kubectl -n "$NAMESPACE" get backups.backups.cozystack.io "$BACKUP_NAME" \
    -o jsonpath='{.status.artifact.uri}' 2>/dev/null || true)
log_success "backup complete: Backup/$BACKUP_NAME (artifact: ${ARTIFACT_URI:-<none>})"

if [[ "${SKIP_RESTORE:-0}" == "1" ]]; then
    log_warning "SKIP_RESTORE=1: stopping after a successful backup."
    exit 0
fi

# --- In-place restore --------------------------------------------------------
log_step "Dropping the sentinel, then restoring in place"
rabbitmqctl_exec "$RABBITMQ_SRC_CR" delete_vhost "$SENTINEL" >/dev/null
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    && { log_error "sentinel vhost still present after delete_vhost"; exit 1; }
log_substep "sentinel dropped from the source"

kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/25-restorejob-in-place.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "in-place restore did not bring back the sentinel vhost"; exit 1; }
log_success "in-place restore round-tripped the sentinel"

# --- Restore-to-copy ---------------------------------------------------------
log_step "Provisioning target RabbitMQ '$RABBITMQ_TARGET_NAME' and restoring the source's definitions into it"
kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/20-rabbitmq-target.yaml"
wait_hr_ready "$RABBITMQ_TARGET_CR" 300
wait_rabbitmq_ready "$RABBITMQ_TARGET_CR" 600
rabbitmq_has_sentinel "$RABBITMQ_TARGET_CR" "$SENTINEL" \
    && { log_error "target already has the sentinel before restore — cannot prove the copy"; exit 1; }

kubectl -n "$NAMESPACE" apply -f "$SCRIPT_DIR/30-restorejob-to-copy.yaml"
wait_for_field restorejobs.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" \
    '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
rabbitmq_has_sentinel "$RABBITMQ_TARGET_CR" "$SENTINEL" \
    || { log_error "to-copy restore did not put the sentinel on the target"; exit 1; }
rabbitmq_has_sentinel "$RABBITMQ_SRC_CR" "$SENTINEL" \
    || { log_error "source lost the sentinel during to-copy restore (source must stay untouched)"; exit 1; }
log_success "to-copy restore placed the sentinel on the target, source untouched"

print_header "Demo complete — definitions round-tripped through object storage"
