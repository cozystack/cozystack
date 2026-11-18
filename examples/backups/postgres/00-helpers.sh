#!/bin/bash
# Shared helpers for the PostgreSQL backup/restore demo.
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
export NAMESPACE="${NAMESPACE:-tenant-root}"
export BUCKET_NAME="${BUCKET_NAME:-pg-backups}"
export PG_SRC_NAME="${PG_SRC_NAME:-pg-src}"
export PG_TARGET_NAME="${PG_TARGET_NAME:-pg-target}"
# The apps/postgres chart names the cnpg.io Cluster (and its Pods'
# cnpg.io/cluster label) after the Helm release, which is postgres-<app>.
export PG_SRC_CLUSTER="postgres-${PG_SRC_NAME}"
export PG_TARGET_CLUSTER="postgres-${PG_TARGET_NAME}"
export STRATEGY_NAME="${STRATEGY_NAME:-cnpg-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-postgres-cnpg}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-pg-src-adhoc}"
# A second ad-hoc BackupJob taken after the PITR marker writes; its completion
# is the deterministic "WAL past the recovery target is archived" gate for the
# point-in-time restore (see run-all.sh step 45).
export BACKUPJOB_POSTMARKER_NAME="${BACKUPJOB_POSTMARKER_NAME:-pg-src-postmarker}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-pg-src-to-pg-target}"
export RESTOREJOB_PITR_NAME="${RESTOREJOB_PITR_NAME:-pg-src-to-pg-target-pitr}"
export RESTOREJOB_UNREACHABLE_NAME="${RESTOREJOB_UNREACHABLE_NAME:-pg-src-to-pg-target-unreachable}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-pg-src-in-place}"
export PLAN_NAME="${PLAN_NAME:-pg-src-daily}"

# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; the demo copies its
# ca.crt into a per-app Secret the barman-cloud ObjectStore trusts via
# endpointCA. The name follows the seaweedfs chart's fullnameOverride
# ("seaweedfs" -> "seaweedfs-ca-cert"); run-all.sh auto-discovers the CA
# Certificate's actual secret when this default is absent, so an upstream
# fullname change does not silently break the copy. On a cluster whose S3
# endpoint is signed by a publicly-trusted CA, set S3_CA_SECRET="" to skip the
# copy and drop endpointCA from the manifests.
export S3_CA_SECRET="${S3_CA_SECRET:-seaweedfs-ca-cert}"
export S3_CA_NAMESPACE="${S3_CA_NAMESPACE:-tenant-root}"
export S3_CA_KEY="${S3_CA_KEY:-ca.crt}"

log_info()    { echo -e "${BLUE}i${NC} $*" >&2; }
log_success() { echo -e "${GREEN}OK${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}!${NC} $*" >&2; }
log_error()   { echo -e "${RED}x${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}> $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  -> $*${NC}" >&2; }

print_header() {
    echo -e "\n${MAGENTA}${BOLD}== $1 ==${NC}\n" >&2
}

# wait_for_field, wait_hr_ready and wait_deleted live in one file shared by
# every backup walkthrough, so a fix to one reaches all of them.
# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"

# Name of the primary pod of a cnpg.io Cluster (the one accepting writes).
cnpg_primary_pod() {
    local cluster="$1"
    kubectl -n "$NAMESPACE" get pod \
        -l "cnpg.io/cluster=${cluster},cnpg.io/instanceRole=primary" \
        -o name 2>/dev/null | head -n1
}

# Run a psql statement on a cnpg.io Cluster's primary. Args: <cluster> <db> <sql>
psql_exec() {
    local cluster="$1" db="$2" sql="$3"
    local pod
    pod=$(cnpg_primary_pod "$cluster")
    [[ -n "$pod" ]] || { log_error "no primary pod for cnpg cluster '$cluster'"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c postgres -- \
        psql -U postgres -d "$db" -tAc "$sql"
}

# Run a psql statement AS the application user over TCP (via the cluster's -rw
# Service), forcing password authentication. psql_exec above connects as the
# in-pod postgres superuser through the local socket (peer auth) and so never
# exercises a user's password; only this path proves the password the
# <release>-credentials Secret advertises actually authenticates. The password is
# read from that chart-managed Secret with jq, not a jsonpath: a username may
# contain a '.', and jsonpath's `{.data['a.b']}` treats the dot as a path step
# and returns nothing, so a dotted user would read an empty password and time out
# on a credential that is fine. jq indexes the key as a literal string. The chart
# names the CNPG Cluster, the -rw Service and the credentials Secret all after the
# release, so the cluster IS the release here - derive the Secret from it rather
# than taking a separate name that a caller can get wrong (a release/app-name
# mix-up read a Secret that never exists and made every login time out).
# Args: <cluster> <user> <db> <sql>
psql_app_exec() {
    local cluster="$1" user="$2" db="$3" sql="$4"
    local pod pw
    pod=$(cnpg_primary_pod "$cluster")
    [[ -n "$pod" ]] || { log_error "no primary pod for cnpg cluster '$cluster'"; return 1; }
    pw=$(kubectl -n "$NAMESPACE" get secret "${cluster}-credentials" -o json \
        | jq -r --arg u "$user" '.data[$u] // empty' | base64 -d)
    [[ -n "$pw" ]] || { log_error "no password for user '${user}' in ${cluster}-credentials"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c postgres -- \
        env PGPASSWORD="$pw" psql -h "${cluster}-rw" -U "$user" -d "$db" -tAc "$sql"
}

# Block until the application user can authenticate against a cluster, or fail
# after <timeout>s. Needed after a to-copy restore: the driver clears
# bootstrap.enabled once recovery converges so the chart's init-job re-runs
# ALTER ROLE ... WITH PASSWORD, converging the recovered roles (which carry the
# source's password hashes) onto the freshly generated <release>-credentials
# Secret. That convergence is asynchronous - a HelmRelease upgrade plus its
# post-upgrade init-job hook - so this is a readiness wait on an eventually
# consistent property, not a retry over a deterministic step.
# Args: <cluster> <user> <db> <timeout>
wait_for_app_login() {
    local cluster="$1" user="$2" db="$3" timeout="${4:-300}"
    local elapsed=0 errfile
    errfile=$(mktemp)
    log_substep "Waiting for user '${user}' to authenticate against ${cluster}..."
    while true; do
        # Success is decided on stdout alone (the '1' from SELECT 1), so a stray
        # notice on stderr cannot read as a failure. stderr is kept in errfile,
        # not discarded: a transient not-yet-converged attempt stays quiet, but
        # the LAST attempt's stderr is what names the cause on timeout (a missing
        # password, or the wrong Secret) - without it a real failure reads as a
        # generic timeout.
        if [[ "$(psql_app_exec "$cluster" "$user" "$db" 'SELECT 1;' 2>"$errfile" | tr -d '[:space:]')" == "1" ]]; then
            rm -f "$errfile"
            log_success "user '${user}' authenticated against ${cluster}"
            return 0
        fi
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for user '${user}' to authenticate against ${cluster}: the credentials Secret advertises a password the roles never received (bootstrap not cleared, or the init-job did not reconcile). Last attempt: $(cat "$errfile")"
            rm -f "$errfile"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}
