#!/bin/bash
# Cleanup: tear down everything provisioned by the demo so the cluster returns
# to its previous state.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup Kafka backup demo"

# Fully qualify every kind: a Cozystack cluster serves four `backups` and two
# `buckets` CRDs, so a bare `kubectl delete backup`/`bucket` may resolve to a
# foreign group whose miss --ignore-not-found then swallows, leaving this demo's
# Cozystack Backup behind for the next run's createJobBackupArtifact to reuse.
kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob.backups.cozystack.io "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob.backups.cozystack.io "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backups.backups.cozystack.io "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "${KAFKA_RESTORE_NAME}-backup-s3" --ignore-not-found
kubectl -n "$NAMESPACE" delete secret "${KAFKA_NAME}-backup-s3" --ignore-not-found
kubectl -n "$NAMESPACE" delete kafka.apps.cozystack.io "$KAFKA_RESTORE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete kafka.apps.cozystack.io "$KAFKA_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete bucket.apps.cozystack.io "$BUCKET_NAME" --ignore-not-found
rm -f "$SCRIPT_DIR/.bucket-info.env" "$SCRIPT_DIR/.source-dump.txt"
kubectl delete backupclass.backups.cozystack.io "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete job.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

# `kubectl delete` returns as soon as deletionTimestamp is set, while the
# HelmRelease uninstall behind each Kafka app (two StatefulSets, ZooKeeper and
# their PVCs) is still draining. Returning here lets the next suite start into a
# namespace whose predecessor is still uninstalling - the neighbouring
# chainsaw/kafka Test's PVC-reclaim step contends with exactly that. Wait for
# the heavy objects to actually go, and with teeth: without `set -e` an absent
# resource cannot abandon the rest of the teardown, so the waits record their
# own failure and the script exits non-zero when the teardown did not settle.
teardown_rc=0
wait_deleted kafka.apps.cozystack.io "$KAFKA_RESTORE_NAME" 180 || teardown_rc=1
wait_deleted kafka.apps.cozystack.io "$KAFKA_NAME" 180 || teardown_rc=1
wait_deleted bucket.apps.cozystack.io "$BUCKET_NAME" 180 || teardown_rc=1

if [[ $teardown_rc -ne 0 ]]; then
    log_error "Cleanup did not settle: see the resources reported above. Failing rather than leaving a half-uninstalled release for the next test to trip over."
    exit 1
fi

log_success "Cleanup complete."
