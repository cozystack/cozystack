#!/bin/bash
# Shared helpers for the ClickHouse backup/restore demo.
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

# Default settings (override via environment).
#
# tenant-root, matching the etcd/mariadb/postgres backup demos: the S3 client
# here is the clickhouse-backup sidecar INSIDE the tenant, and cozystack's
# shared seaweedfs lives in tenant-root. An isolated tenant's Cilium egress
# allowlist blocks a Pod in that tenant from reaching tenant-root's seaweedfs,
# so a release outside tenant-root cannot use the in-cluster endpoint below.
export NAMESPACE="${NAMESPACE:-tenant-root}"
export CLICKHOUSE_NAME="${CLICKHOUSE_NAME:-clickhouse-test}"
export CLICKHOUSE_RESTORE_NAME="${CLICKHOUSE_RESTORE_NAME:-clickhouse-restore}"
export BUCKET_NAME="${BUCKET_NAME:-clickhouse-backups}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-clickhouse-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-altinity}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-clickhouse-backup-job}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-clickhouse-restore-inplace}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-clickhouse-restore-to-copy}"
# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; step 03 copies its
# ca.crt into a per-release Secret the clickhouse-backup sidecar trusts via the
# chart's backup.endpointCA (mounted and picked up through SSL_CERT_DIR). The
# name follows the seaweedfs chart's fullnameOverride ("seaweedfs" ->
# "seaweedfs-ca-cert"); step 03 auto-discovers the CA Certificate's actual
# secret when this default is absent, so an upstream fullname change does not
# silently break the copy. On a cluster whose S3 endpoint is signed by a
# publicly-trusted CA, set S3_CA_SECRET="" to skip the copy and leave
# backup.endpointCA unset.
# Whether the CA Secret name is the user's choice or just this default. Step 03
# treats the two differently: a name the user typed and that does not exist is an
# error worth stopping on, while the default failing to resolve on a cluster with
# no seaweedfs most likely means a publicly-trusted S3 endpoint — a legal input,
# so it warns and leaves backup.endpointCA unset instead of failing.
export S3_CA_SECRET_EXPLICIT="${S3_CA_SECRET:+1}"
export S3_CA_SECRET_EXPLICIT="${S3_CA_SECRET_EXPLICIT:-0}"
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-ca-cert}"
# Independent of NAMESPACE: the shared seaweedfs and its CA live in tenant-root
# even when the demo runs elsewhere.
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"
# Per-release Secret the CA is copied into; referenced by backup.endpointCA.
# Scoped to CLICKHOUSE_NAME so two instances in one namespace don't share a
# name, and stamped with the ownership label below so step 03 refuses to
# overwrite a foreign Secret and cleanup only deletes one this demo created.
export CH_CA_SECRET_NAME="${CH_CA_SECRET_NAME:-${CLICKHOUSE_NAME}-backup-s3-ca}"
export CH_CA_SECRET_LABEL_KEY="cozystack.io/owned-by-demo"
export CH_CA_SECRET_LABEL_VALUE="clickhouse-backup"
# The clickhouse-backup sidecar lives in the application Pod (rendered by the
# chart when backup.enabled=true). Tenants don't manage a separate Secret;
# the chart projects bucket coordinates into <release>-backup-s3 from the
# spec.backup.* values populated by step 04 from the BucketInfo cache.

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }
log_command() { echo -e "${WHITE}  $ $*${NC}" >&2; }

separator() {
    echo -e "\n${CYAN}------------------------------------------------------------${NC}\n" >&2
}

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}== $title ==${NC}\n" >&2
}

# Wait until a JSONPath value on a resource matches the desired string.
#
# The optional 7th argument is a TERMINAL value to bail on: a BackupJob or
# RestoreJob that reaches Failed will never reach Succeeded, so polling out the
# remaining budget only delays the report and — when this runs under Chainsaw —
# risks the step being SIGKILLed at its op timeout with nothing collected
# instead of failing cleanly. Same contract as the postgres/mariadb helpers.
wait_for_field() {
    local resource_type="$1"
    local resource_name="$2"
    local jsonpath="$3"
    local desired="$4"
    local namespace="${5:-}"
    local timeout="${6:-300}"
    local fail_value="${7:-}"

    log_substep "Waiting for $resource_type/$resource_name $jsonpath to become '$desired'..."
    local elapsed=0
    local ns_flag=()
    [[ -n "$namespace" ]] && ns_flag=(-n "$namespace")

    while true; do
        local current
        current=$(kubectl get "$resource_type" "$resource_name" "${ns_flag[@]}" -o jsonpath="$jsonpath" 2>/dev/null || true)
        if [[ "$current" == "$desired" ]]; then
            log_success "$resource_type/$resource_name reached '$desired'"
            return 0
        fi
        if [[ -n "$fail_value" && "$current" == "$fail_value" ]]; then
            log_error "$resource_type/$resource_name reached terminal '$current' (expected '$desired')"
            return 1
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name (current: '$current', expected: '$desired')"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait for a HelmRelease to become Ready, failing fast on Stalled=True.
#
# `kubectl wait hr ... --for=condition=ready` cannot do either half of this: it
# errors out if the HelmRelease does not exist yet (the app CR is reconciled
# asynchronously, so the HR appears a moment after `kubectl apply` returns), and
# it keeps waiting through a terminal Stalled — a chart that will never install
# burns the whole timeout instead of reporting the reason. Same contract as the
# postgres/mariadb helpers.
wait_hr_ready() {
    local name="$1"
    local timeout="${2:-300}"
    local elapsed=0

    log_substep "Waiting for HelmRelease/$name to become Ready..."
    while true; do
        if kubectl -n "$NAMESPACE" get hr "$name" >/dev/null 2>&1; then
            local ready stalled
            ready=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)
            if [[ "$ready" == "True" ]]; then
                log_success "HelmRelease/$name is Ready"
                return 0
            fi
            stalled=$(kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Stalled")].status}' 2>/dev/null || true)
            if [[ "$stalled" == "True" ]]; then
                log_error "HelmRelease/$name is Stalled (terminal): $(kubectl -n "$NAMESPACE" get hr "$name" \
                    -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null)"
                return 1
            fi
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for HelmRelease/$name to become Ready:"
            kubectl -n "$NAMESPACE" get hr "$name" \
                -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' >&2 2>/dev/null || true
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait for a StatefulSet to report a ready replica, tolerating its asynchronous
# creation (the ClickHouse operator creates it after the CHI is reconciled).
wait_sts_ready() {
    local name="$1"
    local timeout="${2:-300}"
    local elapsed=0

    log_substep "Waiting for StatefulSet/$name to report a ready replica..."
    while true; do
        if kubectl -n "$NAMESPACE" get statefulset.apps "$name" >/dev/null 2>&1; then
            local ready
            ready=$(kubectl -n "$NAMESPACE" get statefulset.apps "$name" \
                -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)
            if [[ "${ready:-0}" -ge 1 ]] 2>/dev/null; then
                log_success "StatefulSet/$name has ${ready} ready replica(s)"
                return 0
            fi
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for StatefulSet/$name to become ready"
            # A StatefulSet names its Pods "<sts>-N", so ask for the first
            # replica by name instead of guessing an operator label — and let
            # both streams through, since the point of this branch is to say
            # WHY the Pod never came up. Events carry the reason a Pod is
            # Pending (unschedulable, missing PVC, image pull) that neither the
            # Pod nor the StatefulSet status spells out.
            kubectl -n "$NAMESPACE" get statefulset.apps "$name" -o wide >&2 || true
            kubectl -n "$NAMESPACE" describe pod "${name}-0" >&2 || true
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Wait until a namespaced resource is really gone.
#
# `kubectl wait --for=delete` is not used: it errors out when the resource is
# already absent, which is the normal case on a clean namespace (cleanup.sh is
# idempotent and runs as a pre-clean too), and swallowing that error would also
# swallow a genuine failure. Polling `get --ignore-not-found` treats "already
# gone" and "gone now" as the same success (empty output, exit 0) while a
# retrieval failure — RBAC, API-server, transport — is a non-zero exit that
# must NOT be read as deletion, or cleanup settles on a false success and leaves
# half-uninstalled resources for the next run. Such an error just keeps us
# polling until the resource is confirmed gone or the timeout fires.
wait_deleted() {
    local resource_type="$1"
    local resource_name="$2"
    local timeout="${3:-300}"
    local elapsed=0
    local got

    while true; do
        if got=$(kubectl -n "$NAMESPACE" get "$resource_type" "$resource_name" --ignore-not-found 2>/dev/null) && [[ -z "$got" ]]; then
            [[ $elapsed -gt 0 ]] && log_success "$resource_type/$resource_name is gone"
            return 0
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for $resource_type/$resource_name to be deleted; it is still present after ${timeout}s:"
            kubectl -n "$NAMESPACE" get "$resource_type" "$resource_name" -o wide >&2 || true
            return 1
        fi
        [[ $elapsed -eq 0 ]] && log_substep "Waiting for $resource_type/$resource_name to be deleted..."
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Run a SQL statement against the ClickHouse cluster of the given app instance.
# Args: <release-name> <sql>
# Note: the Cozystack ClickHouse RD prefixes Helm release names with
# "clickhouse-", so the resources rendered by the chart (StatefulSet, Secret,
# etc.) carry that prefix even when the user-facing application name does not.
clickhouse_query() {
    local release="$1"
    local sql="$2"
    kubectl -n "$NAMESPACE" exec -i \
        "statefulset/chi-clickhouse-${release}-clickhouse-0-0" -c clickhouse -- \
        clickhouse-client -u backup --password "$(_clickhouse_password "$release")" -q "$sql"
}

# Read the auto-generated 'backup' user password from the chart-rendered Secret.
_clickhouse_password() {
    local release="$1"
    kubectl -n "$NAMESPACE" get secret "clickhouse-${release}-credentials" \
        -o jsonpath='{.data.backup}' | base64 -d
}
