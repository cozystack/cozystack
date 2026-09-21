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
# Single-token value: the redis_cmd helper word-splits REDIS_ARGS, so a value
# with spaces would break. A UUID-shaped token is enough to prove the exact
# bytes round-tripped through object storage.
export MARKER_VALUE="${MARKER_VALUE:-roundtrip-4f1c9a2b}"
# redis:*-alpine ships redis-cli (SENTINEL discovery + data ops); the seed /
# verify helpers run it as a throwaway Pod via `kubectl run`.
export REDIS_CLI_IMAGE="${REDIS_CLI_IMAGE:-redis:7.4-alpine}"

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

# Wait until a JSONPath value on a resource matches the desired string. An
# optional 7th arg is a TERMINAL failure value: once the field reaches it the
# wait returns 1 immediately instead of polling to the timeout (a BackupJob /
# RestoreJob settles on phase=Failed that never becomes Succeeded).
wait_for_field() {
    local resource_type="$1" resource_name="$2" jsonpath="$3" desired="$4"
    local namespace="${5:-}" timeout="${6:-300}" fail_value="${7:-}"

    log_substep "Waiting for $resource_type/$resource_name $jsonpath to become '$desired'..."
    local elapsed=0 ns_flag=()
    [[ -n "$namespace" ]] && ns_flag=(-n "$namespace")
    while true; do
        local current
        current=$(kubectl get "$resource_type" "$resource_name" "${ns_flag[@]}" -o jsonpath="$jsonpath" 2>/dev/null || true)
        [[ "$current" == "$desired" ]] && { log_success "$resource_type/$resource_name reached '$desired'"; return 0; }
        [[ -n "$fail_value" && "$current" == "$fail_value" ]] && { log_error "$resource_type/$resource_name reached terminal '$current' (expected '$desired')"; return 1; }
        (( elapsed >= timeout )) && { log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"; return 1; }
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait for a HelmRelease to become Ready, with an existence backstop (the apps
# controller creates the HR asynchronously) and a fail-fast on Stalled=True.
wait_hr_ready() {
    local name="$1" timeout="${2:-300}" elapsed=0
    log_substep "Waiting for HelmRelease/$name to become Ready..."
    while true; do
        if kubectl -n "$NAMESPACE" get hr "$name" >/dev/null 2>&1; then
            local ready stalled
            ready=$(kubectl -n "$NAMESPACE" get hr "$name" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
            [[ "$ready" == "True" ]] && { log_success "HelmRelease/$name is Ready"; return 0; }
            stalled=$(kubectl -n "$NAMESPACE" get hr "$name" -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' 2>/dev/null || true)
            [[ "$stalled" == "True" ]] && { log_error "HelmRelease/$name is Stalled (terminal)"; return 1; }
        fi
        (( elapsed >= timeout )) && { log_error "Timeout waiting for HelmRelease/$name to become Ready"; return 1; }
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# The cozystack redis chart names its RedisFailover (hence the operator's rfr-/
# rfs- Services and the -auth Secret) redis-<app>, mirroring the strategy driver
# that derives the same base from .Release.Name. So app X's password lives in
# redis-X-auth. Empty when auth is disabled.
redis_pw() {
    kubectl -n "$NAMESPACE" get secret "redis-$1-auth" -o jsonpath='{.data.password}' 2>/dev/null | base64 -d
}

# Run a redis-cli command against an application's CURRENT master. Master
# discovery goes through the operator's sentinel Service (rfs-redis-<app>, master
# name "mymaster"), so a write always lands on the writable node even after a
# failover. Args after the app name are passed verbatim to redis-cli.
#   redis_cmd redis-test SET sentinel:marker hello
redis_cmd() {
    local app="$1"; shift
    kubectl -n "$NAMESPACE" run "redis-cli-$RANDOM" \
        --image="$REDIS_CLI_IMAGE" --restart=Never --rm -i --quiet \
        --env="REDIS_PW=$(redis_pw "$app")" \
        --env="APP=redis-$app" \
        --env="REDIS_ARGS=$*" \
        --command -- sh -c '
            addr=$(redis-cli -h "rfs-$APP" -p 26379 sentinel get-master-addr-by-name mymaster)
            h=$(echo "$addr" | sed -n 1p); p=$(echo "$addr" | sed -n 2p)
            [ -n "$h" ] && [ -n "$p" ] || { echo "no master from sentinel rfs-$APP" >&2; exit 1; }
            auth=""; [ -n "$REDIS_PW" ] && auth="-a $REDIS_PW --no-auth-warning"
            exec redis-cli -h "$h" -p "$p" $auth $REDIS_ARGS
        ' 2>/dev/null | tr -d '[:space:]'
}

# Block until the RedisFailover has an elected master reachable through sentinel.
wait_redis_master() {
    local app="$1" timeout="${2:-300}" elapsed=0
    log_substep "Waiting for '$app' master election via sentinel..."
    while true; do
        local h
        h=$(redis_cmd "$app" PING 2>/dev/null || true)
        [[ "$h" == "PONG" ]] && { log_success "'$app' master is reachable"; return 0; }
        (( elapsed >= timeout )) && { log_error "Timeout waiting for '$app' master"; return 1; }
        sleep 5
        elapsed=$((elapsed + 5))
    done
}
