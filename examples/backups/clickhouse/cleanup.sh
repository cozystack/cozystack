#!/bin/bash
# Cleanup: tear down everything provisioned by the demo so the cluster returns
# to its previous state.
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Cleanup ClickHouse backup demo"

kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete restorejob "$RESTOREJOB_INPLACE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete backup "$BACKUPJOB_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete clickhouse "$CLICKHOUSE_RESTORE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete clickhouse "$CLICKHOUSE_NAME" --ignore-not-found
kubectl -n "$NAMESPACE" delete bucket "$BUCKET_NAME" --ignore-not-found

# `kubectl delete` on these returns as soon as deletionTimestamp is set, while
# the HelmRelease uninstall behind each one (a ClickHouseInstallation, a Keeper
# installation and their PVCs) is still running. Returning here has two costs:
# the next suite starts while helm-controller workers are still draining this
# one, and on the pre-clean path step 04 can re-apply an application whose
# predecessor has not finished uninstalling. So wait for the objects to actually
# go, with teeth — a teardown that silently does not settle is what leaks into
# later tests.
# This script deliberately runs without `set -e` so one absent resource cannot
# abandon the rest of the teardown, which means the waits have to record their
# own failure and surface it at the end.
teardown_rc=0
wait_deleted clickhouse "$CLICKHOUSE_RESTORE_NAME" 180 || teardown_rc=1
wait_deleted clickhouse "$CLICKHOUSE_NAME" 180 || teardown_rc=1
wait_deleted bucket "$BUCKET_NAME" 180 || teardown_rc=1
# The S3 endpoint CA copied out of the seaweedfs CA Secret by step 03. Delete
# it only when it carries this demo's ownership label, so a foreign Secret that
# happens to share the name survives. The source Secret in S3_CA_NAMESPACE is
# the platform's and is never touched.
if ca_owner=$(kubectl -n "$NAMESPACE" get secret "$CH_CA_SECRET_NAME" \
        -o jsonpath="{.metadata.labels.${CH_CA_SECRET_LABEL_KEY//./\\.}}" 2>/dev/null) \
        && [[ "$ca_owner" == "$CH_CA_SECRET_LABEL_VALUE" ]]; then
    kubectl -n "$NAMESPACE" delete secret "$CH_CA_SECRET_NAME" --ignore-not-found
fi
rm -f "$SCRIPT_DIR/.bucket-info.env"
kubectl delete backupclass "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete altinity.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found

if [[ $teardown_rc -ne 0 ]]; then
    log_error "Cleanup did not settle: see the resources reported above. Failing rather than leaving a half-uninstalled release for the next test to trip over."
    exit 1
fi

log_success "Cleanup complete."
