#!/bin/bash
# Shared helpers for the Redis (spotahome RedisFailover) backup/restore demo.
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export BLUE='\033[0;34m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export WHITE='\033[1;37m'
export NC='\033[0m'
export BOLD='\033[1m'

# Default settings (override via environment). NAMESPACE is tenant-root because
# the demo provisions its Bucket there (the in-cluster seaweedfs the backup Job
# reaches lives in tenant-root, and the default e2e tenant-test is egress-
# isolated from it).
export NAMESPACE="${NAMESPACE:-tenant-root}"
export REDIS_NAME="${REDIS_NAME:-redis-test}"
export REDIS_RESTORE_NAME="${REDIS_RESTORE_NAME:-redis-restore}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-redis-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-redis-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-redis-restore-to-copy}"
# This demo provisions its own Bucket + Redis strategy + BackupClass rather than
# reusing the platform cozy-default flow, so the round-trip is self-contained
# (its S3 objects tear down with the demo) and works in CI, where the shared
# system bucket's advertised external endpoint is not routable. The Bucket
# controller materialises the credentials Secret as "bucket-<bucket>-<user>".
export BUCKET_NAME="${BUCKET_NAME:-redis-backups}"
export BUCKET_USER="${BUCKET_USER:-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-redis-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-redis-default}"
# The strategy Pod's curl reads AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from
# this Secret and verifies the S3 endpoint's CA from ca.crt in the CA Secret;
# run-all.sh materialises both from the provisioned Bucket before dispatch.
export CREDS_SECRET="${CREDS_SECRET:-redis-backup-creds}"
export CA_SECRET="${CA_SECRET:-redis-backup-ca}"
# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; run-all.sh copies its
# ca.crt into CA_SECRET, which the strategy Pod mounts at /etc/s3-ca. The name
# follows the seaweedfs chart's fullnameOverride ("seaweedfs" ->
# "seaweedfs-ca-cert"); run-all.sh auto-discovers the CA Certificate's actual
# secret when this default is absent. On a cluster whose S3 endpoint is signed
# by a publicly-trusted CA, set S3_CA_SECRET="" to skip the copy.
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-ca-cert}"
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"
export MARKER_KEY="${MARKER_KEY:-sentinel:marker}"
# Single-token value: redis_cmd strips all whitespace from the reply, so a
# value with spaces would not compare equal. A UUID-shaped token is enough to
# prove the exact bytes round-tripped through object storage.
export MARKER_VALUE="${MARKER_VALUE:-roundtrip-4f1c9a2b}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}== $title ==${NC}\n" >&2
}

# wait_for_field, wait_hr_ready and wait_deleted live in one file shared by
# every backup walkthrough, so a fix to one reaches all of them.
# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"

# The cozystack redis chart names its RedisFailover (hence the operator's rfr-/
# rfs- Services and the -auth Secret) redis-<app>, mirroring the strategy driver
# that derives the same base from .Release.Name. So app X's password lives in
# redis-X-auth. The chart renders that Secret only with authEnabled, so its
# absence reads as no password; any other error still fails the read.
redis_pw() {
    kubectl -n "$NAMESPACE" get secret "redis-$1-auth" --ignore-not-found -o jsonpath='{.data.password}' | base64 -d
}

# Run a redis-cli command against an application's CURRENT master. Master
# discovery asks a sentinel of the operator's rfs-redis-<app> Deployment
# (master name "mymaster"), so a write always lands on the writable node even
# after a failover. Args after the app name are passed verbatim to redis-cli.
#   redis_cmd redis-test SET sentinel:marker hello
#
# The command runs by `kubectl exec` in a sentinel Pod, whose image carries
# redis-cli. A throwaway `kubectl run -i` Pod is not usable here: its stdout
# comes back over an attach, which only carries what the container writes after
# the attach is established, and kubectl falls back to the Pod log only when
# the attach fails. A redis-cli that finishes in between comes back as an
# empty reply with exit 0. An exec'd process writes into pipes of its own, so
# its output cannot be missed that way.
#
# The password travels on stdin rather than in argv, which the exec request
# URL carries into the API server's audit log. kubectl's and redis-cli's
# stderr stay attached, so a failed read says why instead of reading as ''.
redis_cmd() {
    local app="$1" pw; shift
    pw=$(redis_pw "$app")
    printf '%s\n' "$pw" | kubectl -n "$NAMESPACE" exec -i "deploy/rfs-redis-$app" -c sentinel -- sh -c '
            IFS= read -r pw || true
            addr=$(redis-cli -p 26379 sentinel get-master-addr-by-name mymaster) || exit 1
            h=$(echo "$addr" | sed -n 1p); p=$(echo "$addr" | sed -n 2p)
            [ -n "$h" ] && [ -n "$p" ] || { echo "sentinel knows no master for mymaster" >&2; exit 1; }
            [ -z "$pw" ] || export REDISCLI_AUTH="$pw"
            exec redis-cli -h "$h" -p "$p" "$@"
        ' sh "$@" | tr -d '[:space:]'
}

# Block until the RedisFailover has an elected master reachable through sentinel.
wait_redis_master() {
    local app="$1" timeout="${2:-300}" elapsed=0
    log_substep "Waiting for '$app' master election via sentinel..."
    while true; do
        local h
        h=$(redis_cmd "$app" PING || true)
        [[ "$h" == "PONG" ]] && { log_success "'$app' master is reachable"; return 0; }
        (( elapsed >= timeout )) && { log_error "Timeout waiting for '$app' master"; return 1; }
        sleep 5
        elapsed=$((elapsed + 5))
    done
}
