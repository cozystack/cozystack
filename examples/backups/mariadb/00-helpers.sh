#!/bin/bash
# Shared helpers for the MariaDB backup/restore demo.
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
export BUCKET_NAME="${BUCKET_NAME:-mariadb-backups}"
# Bucket user declared in 00-bucket.yaml; the COSI flow materialises the
# credentials Secret as "bucket-<bucket>-<user>".
export BUCKET_USER="${BUCKET_USER:-backup}"
export MARIADB_SRC_NAME="${MARIADB_SRC_NAME:-mariadb-src}"
export MARIADB_TARGET_NAME="${MARIADB_TARGET_NAME:-mariadb-target}"
# The mariadb-rd ApplicationDefinition renders the HelmRelease with
# releaseName = "mariadb-" + appName (release.prefix in
# packages/system/mariadb-rd/cozyrds/mariadb.yaml), so the operator-side
# k8s.mariadb.com/MariaDB CR (and its primary Service) is mariadb-<app>.
export MARIADB_SRC_CR="mariadb-${MARIADB_SRC_NAME}"
export MARIADB_TARGET_CR="mariadb-${MARIADB_TARGET_NAME}"
export STRATEGY_NAME="${STRATEGY_NAME:-mariadb-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-mariadb-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-mariadb-src-adhoc}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-mariadb-src-to-mariadb-target}"
export PLAN_NAME="${PLAN_NAME:-mariadb-src-daily}"
# App user the demo writes and reads the sentinel row as. Its password is
# minted through the credentials API by mysql_exec on first use.
export MARIADB_APP_USER="${MARIADB_APP_USER:-app}"

# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; the demo copies its
# ca.crt into a per-app Secret the mariadb-operator Backup CR trusts via
# storage.s3.tls.caSecretKeyRef. The name follows the seaweedfs chart's
# fullnameOverride ("seaweedfs" -> "seaweedfs-ca-cert"); run-all.sh
# auto-discovers the CA Certificate's actual secret when this default is
# absent, so an upstream fullname change does not silently break the copy. On
# a cluster whose S3 endpoint is signed by a publicly-trusted CA, set
# S3_CA_SECRET="" to skip the copy and drop the tls block from the manifests.
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

# Name of the primary pod of a k8s.mariadb.com/MariaDB CR (the writer).
# mariadb-operator 25.10.x reports the primary identity on
# MariaDB.status.currentPrimary rather than a pod label, so read it there.
mariadb_primary_pod() {
    local cr="$1"
    kubectl -n "$NAMESPACE" get mariadbs.k8s.mariadb.com "$cr" \
        -o jsonpath='{.status.currentPrimary}' 2>/dev/null
}

# Password of the application user, minted once through the credentials API
# and cached for the rest of the run: a mint replaces the account's password,
# so minting on every call would invalidate the previous one. The Credential is
# named <release>.<account>, where <account> is the user name lowercased with
# every character outside [a-z0-9-] replaced by "-", and the MariaDB CR is
# named after the release. The cache is a private run-scoped directory
# (mktemp -d is 0700), so a later run does not read a password a mint has
# since replaced, and no other user on the host can read it.
# Args: <cr-name> <user>
export COZY_PW_CACHE_DIR="${COZY_PW_CACHE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/cozy-pw.XXXXXXXX")}"

app_password() {
    local cr="$1" user="$2"
    local acct cache
    acct=$(printf '%s' "$user" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9-]/-/g')
    cache="${COZY_PW_CACHE_DIR}/${cr}-${acct}"
    if [[ ! -s "$cache" ]]; then
        kubectl create --raw "/apis/core.cozystack.io/v1alpha1/namespaces/${NAMESPACE}/credentials/${cr}.${acct}/mint" \
            -f <(printf '{"apiVersion":"core.cozystack.io/v1alpha1","kind":"CredentialRequest"}') \
            | jq -r '.status.password // empty' > "$cache"
    fi
    [[ -s "$cache" ]] || { log_error "no password minted for user '${user}' of ${cr}"; return 1; }
    cat "$cache"
}

# Run a single SQL statement against a MariaDB CR's primary as the app user,
# routing through the operator-managed primary Service (mariadb-<app>-primary).
# Args: <cr-name> <sql>
mysql_exec() {
    local cr="$1" sql="$2"
    local pod pass
    pod=$(mariadb_primary_pod "$cr")
    [[ -n "$pod" ]] || { log_error "no primary pod for MariaDB CR '$cr'"; return 1; }
    pass=$(app_password "$cr" "$MARIADB_APP_USER") || return 1
    # The password goes to the client on stdin: exec arguments travel in the
    # request URI, which the audit log records at any level.
    printf '%s\n' "$pass" | kubectl -n "$NAMESPACE" exec -i "$pod" -c mariadb -- \
        sh -c 'IFS= read -r MYSQL_PWD; export MYSQL_PWD; exec mariadb "$@"' sh \
        -u"$MARIADB_APP_USER" -h "${cr}-primary" -e "$sql"
}

# Block until the app user authenticates against a CR, or fail after
# <timeout>s. The first mint rewrites the account Secret and mariadb-operator
# applies it through the Secret's watch label, asynchronously, so this is a
# readiness wait on an eventually consistent property, not a retry over a
# deterministic step. Args: <cr-name> [timeout]
wait_app_login() {
    local cr="$1" timeout="${2:-120}" elapsed=0
    until mysql_exec "$cr" "SELECT 1;" >/dev/null 2>&1; do
        if [[ $elapsed -ge $timeout ]]; then
            log_error "Timeout waiting for '${MARIADB_APP_USER}' to authenticate against ${cr}: the minted password never reached the account (is the User Ready?)"
            return 1
        fi
        sleep 5
        elapsed=$((elapsed + 5))
    done
}

# Authenticate as root against a MariaDB CR's primary over TCP, using the
# chart-generated root password in <cr>-credentials, a platform-only Secret
# that only a platform administrator can read. root is the sharp edge on a
# restore into a copy: the operator only applies rootPasswordSecretKeyRef at
# datadir bootstrap, so if a restore overwrote the grant table with the source's
# root hash the target's advertised root would be wrong for good. Args: <cr-name>
mysql_root_login() {
    local cr="$1"
    local pod pass
    pod=$(mariadb_primary_pod "$cr")
    [[ -n "$pod" ]] || { log_error "no primary pod for MariaDB CR '$cr'"; return 1; }
    pass=$(kubectl -n "$NAMESPACE" get secret "${cr}-credentials" \
        -o "jsonpath={.data['root']}" | base64 -d)
    [[ -n "$pass" ]] || { log_error "no root password in ${cr}-credentials"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c mariadb -- \
        mariadb -uroot -p"$pass" -h "${cr}-primary" -e "SELECT 1;"
}

# Run a single SQL statement against a MariaDB CR's primary as root over TCP.
# Used for statements the app user is not privileged for (CREATE USER / GRANT),
# e.g. seeding an out-of-band account the chart does not declare. root has no dot,
# so a plain jsonpath read of its key is safe here. Args: <cr-name> <sql>
mysql_root_exec() {
    local cr="$1" sql="$2"
    local pod pass
    pod=$(mariadb_primary_pod "$cr")
    [[ -n "$pod" ]] || { log_error "no primary pod for MariaDB CR '$cr'"; return 1; }
    pass=$(kubectl -n "$NAMESPACE" get secret "${cr}-credentials" \
        -o "jsonpath={.data['root']}" | base64 -d)
    [[ -n "$pass" ]] || { log_error "no root password in ${cr}-credentials"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c mariadb -- \
        mariadb -uroot -p"$pass" -h "${cr}-primary" -e "$sql"
}
