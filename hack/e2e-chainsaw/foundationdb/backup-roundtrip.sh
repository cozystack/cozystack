#!/bin/bash
# FoundationDB backup/restore round-trip through the platform cozy-default
# BackupClass, run from the foundationdb-3-backup-roundtrip Test:
#
#   1. a source and an empty target FoundationDB come up, and a sentinel key
#      is written to the source;
#   2. two consecutive BackupJobs both reach Succeeded, each with its own
#      blob-store directory (the second stops the first's backup);
#   3. a RestoreJob of the second backup into the target reaches Succeeded
#      and the sentinel reads back byte for byte;
#   4. deleting the first Backup removes its directory and leaves the
#      second's in place.
#
# Usage: backup-roundtrip.sh run|cleanup   (NAMESPACE must be set)
#
# `cleanup` is idempotent and is what the Test's finally step calls: deleting
# the Backups runs the driver's delete Jobs, so the bucket is left as found.
set -euo pipefail

: "${NAMESPACE:?NAMESPACE must be set}"
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

log_substep() { echo "  -> $*"; }
log_success() { echo "  OK $*"; }
log_error() { echo "  ERROR $*" >&2; }
# shellcheck source=examples/backups/_lib/wait-helpers.sh
. examples/backups/_lib/wait-helpers.sh

SRC=fdb-rt-src
DST=fdb-rt-copy
BJ1=fdb-rt-bj1
BJ2=fdb-rt-bj2
RJ=fdb-rt-restore
SENTINEL_KEY=/e2e/fdb-roundtrip
OWNED_BY=backups.cozystack.io/owned-by.BackupJobName

apply_app() {
	kubectl -n "$NAMESPACE" apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: FoundationDB
metadata:
  name: $1
spec:
  cluster:
    version: "7.3.63"
    processCounts:
      storage: 1
      stateless: -1
      cluster_controller: 1
    redundancyMode: "single"
    storageEngine: "ssd-2"
    faultDomain:
      key: "foundationdb.org/none"
      valueFrom: "\$FDB_ZONE_ID"
  storage:
    size: "1Gi"
    storageClass: ""
  resourcesPreset: "small"
  monitoring:
    enabled: false
  imageType: "unified"
  automaticReplacements: true
EOF
}

apply_backupjob() {
	kubectl -n "$NAMESPACE" apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: $1
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: FoundationDB
    name: $SRC
  backupClassName: cozy-default
EOF
}

fdbcli_exec() {
	local pod
	pod=$(kubectl -n "$NAMESPACE" get pods \
		-l "foundationdb.org/fdb-cluster-name=foundationdb-$1,foundationdb.org/fdb-process-class=cluster_controller" \
		--field-selector=status.phase=Running -o jsonpath='{.items[0].metadata.name}')
	[[ -n "$pod" ]] || { log_error "no running cluster_controller pod for foundationdb-$1"; return 1; }
	timeout -k 5 60 kubectl -n "$NAMESPACE" exec "$pod" -c foundationdb -- \
		fdbcli --cluster-file=/var/dynamic-conf/fdb.cluster --exec "$2"
}

backup_uri() {
	kubectl -n "$NAMESPACE" get backup "$1" -o json | jq -r '.spec.driverMetadata["apps.foundationdb.org/storage-uri"] // ""'
}

# fdbbackup describe exits 0 for a directory holding a backup and prints
# "Backup does not exist" for one that is gone; any other failure is neither
# answer and fails the run. It runs in the second backup's agent pod, which
# carries the platform credentials and CA.
describe_dir() {
	local deploy out rc=0
	deploy=$(kubectl -n "$NAMESPACE" get deploy -o name | grep -- "^deployment.apps/$BJ2-.*-backup-agents$" | head -n1)
	[[ -n "$deploy" ]] || { log_error "no backup agents Deployment for $BJ2"; return 2; }
	out=$(timeout -k 5 120 kubectl -n "$NAMESPACE" exec "$deploy" -c foundationdb -- fdbbackup describe -d "$1" 2>&1) || rc=$?
	if [[ $rc -eq 0 ]]; then
		echo present
	elif grep -q 'Backup does not exist' <<<"$out"; then
		echo absent
	else
		log_error "fdbbackup describe failed for $1: $out"
		return 2
	fi
}

run() {
	local value backup1 backup2 uri1 uri2 state1 state2 got
	value="roundtrip-$(date -u +%Y%m%dT%H%M%SZ)"

	apply_app "$SRC"
	apply_app "$DST"
	# 660s: above the 600s install timeout cozystack-api gives these releases
	# (no helm-install-timeout override on foundationdb-rd), per the HR-Ready
	# sizing in docs/agents/e2e-testing.md.
	wait_hr_ready "foundationdb-$SRC" 660
	wait_hr_ready "foundationdb-$DST" 660
	wait_for_field foundationdbclusters.apps.foundationdb.org "foundationdb-$SRC" '{.status.health.available}' true "$NAMESPACE" 600
	wait_for_field foundationdbclusters.apps.foundationdb.org "foundationdb-$DST" '{.status.health.available}' true "$NAMESPACE" 600

	fdbcli_exec "$SRC" "writemode on; set $SENTINEL_KEY $value"

	# A BackupJob succeeds once its own directory is restorable; 900s covers
	# agent start, the first snapshot and the driver's 60s status refresh.
	apply_backupjob "$BJ1"
	wait_for_field backupjob "$BJ1" '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
	apply_backupjob "$BJ2"
	wait_for_field backupjob "$BJ2" '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed

	backup1=$(kubectl -n "$NAMESPACE" get backupjob "$BJ1" -o jsonpath='{.status.backupRef.name}')
	backup2=$(kubectl -n "$NAMESPACE" get backupjob "$BJ2" -o jsonpath='{.status.backupRef.name}')
	uri1=$(backup_uri "$backup1")
	uri2=$(backup_uri "$backup2")
	[[ "$uri1" == *"/$NAMESPACE/$BJ1?"* ]] || { log_error "first backup does not name its own directory: $uri1"; return 1; }
	[[ "$uri2" == *"/$NAMESPACE/$BJ2?"* ]] || { log_error "second backup does not name its own directory: $uri2"; return 1; }

	kubectl -n "$NAMESPACE" apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: $RJ
spec:
  backupRef:
    name: $backup2
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: FoundationDB
    name: $DST
EOF
	wait_for_field restorejob "$RJ" '{.status.phase}' Succeeded "$NAMESPACE" 900 Failed
	got=$(fdbcli_exec "$DST" "get $SENTINEL_KEY")
	if ! grep -qF "is \`$value'" <<<"$got"; then
		log_error "sentinel did not survive the restore: $got"
		return 1
	fi
	log_success "sentinel restored into $DST"

	kubectl -n "$NAMESPACE" delete backup "$backup1" --wait=false
	wait_deleted backup "$backup1" 600
	state1=$(describe_dir "$uri1")
	state2=$(describe_dir "$uri2")
	[[ "$state1" == absent ]] || { log_error "deleting the first Backup left its directory: $uri1"; return 1; }
	[[ "$state2" == present ]] || { log_error "deleting the first Backup removed the second's directory: $uri2"; return 1; }
	log_success "FoundationDB round-trip verified"
}

cleanup() {
	kubectl -n "$NAMESPACE" delete restorejob "$RJ" --ignore-not-found --wait=false
	# Each Backup's finalizer stops its operator CR and deletes its directory.
	local bj b
	for bj in "$BJ1" "$BJ2"; do
		b=$(kubectl -n "$NAMESPACE" get backupjob "$bj" --ignore-not-found -o jsonpath='{.status.backupRef.name}')
		if [[ -n "$b" ]]; then
			kubectl -n "$NAMESPACE" delete backup "$b" --ignore-not-found --wait=false
			wait_deleted backup "$b" 600
		fi
	done
	kubectl -n "$NAMESPACE" delete backupjob "$BJ1" "$BJ2" --ignore-not-found --wait=false
	kubectl -n "$NAMESPACE" delete foundationdbbackups.apps.foundationdb.org -l "$OWNED_BY in ($BJ1,$BJ2)" --ignore-not-found
	kubectl -n "$NAMESPACE" delete foundationdbrestores.apps.foundationdb.org -l "$OWNED_BY=$RJ" --ignore-not-found
	kubectl -n "$NAMESPACE" delete foundationdbs.apps.cozystack.io "$SRC" "$DST" --ignore-not-found --wait=false
	wait_deleted hr "foundationdb-$SRC" 600
	wait_deleted hr "foundationdb-$DST" 600
}

case "${1:-}" in
run) run ;;
cleanup) cleanup ;;
*) echo "usage: $0 run|cleanup" >&2; exit 2 ;;
esac
