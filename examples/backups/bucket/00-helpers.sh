#!/bin/bash
# Shared helpers for the S3 Bucket backup/restore demo.
# Source this file: source "$(dirname "$0")/00-helpers.sh"

export GREEN='\033[0;32m'
export RED='\033[0;31m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export NC='\033[0m'

# Default settings (override via environment).
export NAMESPACE="${NAMESPACE:-tenant-root}"
export SRC_APP="${SRC_APP:-bktsrc}"
export DST_APP="${DST_APP:-bktdst}"
export TGT_APP="${TGT_APP:-bkttgt}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-bucket-test}"
export STRATEGY_NAME="${STRATEGY_NAME:-bucket-strategy-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-bktsrc-adhoc}"
export PLAN_NAME="${PLAN_NAME:-bktsrc-daily}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-bktsrc-in-place}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-bktsrc-to-bkttgt}"
# The bucket-rd ApplicationDefinition wraps every Bucket app in a "bucket-"
# Helm release, so the COSI objects are named bucket-<app>.
export SRC_RELEASE="bucket-${SRC_APP}"
export DST_RELEASE="bucket-${DST_APP}"
export TGT_RELEASE="bucket-${TGT_APP}"
# In-cluster S3 endpoint. The COSI BucketInfo advertises the external ingress
# host, which in-cluster Pods (and this port-forward's target) cannot always
# reach or TLS-validate, so the strategy and the sentinel client both talk to
# the Service directly over its self-signed cert.
export S3_SVC="${S3_SVC:-seaweedfs-s3}"
export S3_PORT="${S3_PORT:-8333}"
# The credentials Secret the demo materialises from the destination bucket's
# `backup` user, mirroring how the platform projects cozy-backups-creds.
export DEST_CREDS_SECRET="${DEST_CREDS_SECRET:-bucket-backup-creds}"
# The sentinel object the round-trip proves survives object storage.
export SENTINEL_KEY="${SENTINEL_KEY:-sentinel/marker.txt}"
export SENTINEL_VALUE="${SENTINEL_VALUE:-cozy-bucket-backup-roundtrip-$(date +%s)}"

# k applies the namespace to a kubectl invocation.
k() { kubectl -n "$NAMESPACE" "$@"; }

# controller_image discovers the backupstrategy-controller image running in the
# cluster; the mirror Job reuses it to invoke `s3-mirror`.
controller_image() {
  kubectl get deploy -A \
    -o jsonpath='{range .items[*]}{range .spec.template.spec.containers[*]}{.image}{"\n"}{end}{end}' \
    | grep 'backupstrategy-controller' | head -1
}

# bucketinfo_field reads one spec.secretS3/spec field from a COSI BucketInfo
# Secret. Usage: bucketinfo_field <secret> <jq-path>
bucketinfo_field() {
  k get secret "$1" -o jsonpath='{.data.BucketInfo}' | base64 -d | jq -r "$2"
}

# wait_hr_ready blocks until a HelmRelease reports Ready=True, failing fast if
# it goes Stalled. Deterministic: no blind sleeps.
wait_hr_ready() {
  local name="$1" timeout="${2:-300}" elapsed=0
  while true; do
    local ready stalled
    ready=$(k get helmrelease "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
    stalled=$(k get helmrelease "$name" -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' 2>/dev/null || true)
    [ "$ready" = "True" ] && return 0
    [ "$stalled" = "True" ] && { echo -e "${RED}HelmRelease $name Stalled${NC}" >&2; return 1; }
    [ "$elapsed" -ge "$timeout" ] && { echo -e "${RED}timeout waiting HelmRelease $name Ready${NC}" >&2; return 1; }
    sleep 5; elapsed=$((elapsed + 5))
  done
}

# wait_field polls a resource field until it equals want, bailing on a terminal
# phase=Failed so a broken backup reports in seconds, not at the timeout.
# Usage: wait_field <kind> <name> <jsonpath> <want> [timeout]
wait_field() {
  local kind="$1" name="$2" path="$3" want="$4" timeout="${5:-300}" elapsed=0
  while true; do
    local got phase
    got=$(k get "$kind" "$name" -o jsonpath="$path" 2>/dev/null || true)
    [ "$got" = "$want" ] && return 0
    phase=$(k get "$kind" "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [ "$phase" = "Failed" ] && { echo -e "${RED}$kind/$name reached phase=Failed${NC}" >&2; k get "$kind" "$name" -o yaml >&2; return 1; }
    [ "$elapsed" -ge "$timeout" ] && { echo -e "${RED}timeout waiting $kind/$name $path=$want (got '$got')${NC}" >&2; return 1; }
    sleep 5; elapsed=$((elapsed + 5))
  done
}
