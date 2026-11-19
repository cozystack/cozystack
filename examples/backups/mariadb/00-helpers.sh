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
# chart-generated (the charts no longer accept a plaintext password) and read
# from the release's <cr>-credentials Secret by mysql_exec at call time.
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

# Run a single SQL statement against a MariaDB CR's primary as the app user,
# routing through the operator-managed primary Service (mariadb-<app>-primary).
# Args: <cr-name> <sql>
mysql_exec() {
    local cr="$1" sql="$2"
    local pod pass
    pod=$(mariadb_primary_pod "$cr")
    [[ -n "$pod" ]] || { log_error "no primary pod for MariaDB CR '$cr'"; return 1; }
    pass=$(kubectl -n "$NAMESPACE" get secret "${cr}-credentials" -o json \
        | jq -r --arg u "$MARIADB_APP_USER" '.data[$u] // empty' | base64 -d)
    [[ -n "$pass" ]] || { log_error "no password for '$MARIADB_APP_USER' in ${cr}-credentials"; return 1; }
    kubectl -n "$NAMESPACE" exec "$pod" -c mariadb -- \
        mariadb -u"$MARIADB_APP_USER" -p"$pass" -h "${cr}-primary" -e "$sql"
}

# Authenticate as root against a MariaDB CR's primary over TCP, using the
# chart-generated root password in <cr>-credentials. root is the sharp edge on a
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
