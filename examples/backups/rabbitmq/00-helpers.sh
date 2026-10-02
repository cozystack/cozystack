#!/bin/bash
# Shared helpers for the RabbitMQ definitions backup/restore demo.
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
export RABBITMQ_SRC_NAME="${RABBITMQ_SRC_NAME:-rabbitmq-src}"
export RABBITMQ_TARGET_NAME="${RABBITMQ_TARGET_NAME:-rabbitmq-target}"
# The rabbitmq-rd ApplicationDefinition renders the HelmRelease with
# releaseName = "rabbitmq-" + appName (release.prefix in
# packages/system/rabbitmq-rd/cozyrds/rabbitmq.yaml), and the chart sets the
# rabbitmq.com/RabbitmqCluster metadata.name to .Release.Name, so the
# operator-side CR (its Service, default-user Secret and server pods) is
# rabbitmq-<app>.
export RABBITMQ_SRC_CR="rabbitmq-${RABBITMQ_SRC_NAME}"
export RABBITMQ_TARGET_CR="rabbitmq-${RABBITMQ_TARGET_NAME}"
# This demo provisions its own Bucket + Rabbitmq strategy + BackupClass rather
# than reusing the platform cozy-default flow, so the round-trip is
# self-contained and its S3 objects are torn down with the demo. The Bucket
# controller materialises the credentials Secret as "bucket-<bucket>-<user>".
export BUCKET_NAME="${BUCKET_NAME:-rabbitmq-backups}"
export BUCKET_USER="${BUCKET_USER:-backup}"
export STRATEGY_NAME="${STRATEGY_NAME:-rabbitmq-strategy-default}"
export BACKUPCLASS_NAME="${BACKUPCLASS_NAME:-rabbitmq-default}"
export BACKUPJOB_NAME="${BACKUPJOB_NAME:-rabbitmq-src-adhoc}"
export RESTOREJOB_INPLACE_NAME="${RESTOREJOB_INPLACE_NAME:-rabbitmq-src-in-place}"
export RESTOREJOB_TOCOPY_NAME="${RESTOREJOB_TOCOPY_NAME:-rabbitmq-src-to-rabbitmq-target}"
export PLAN_NAME="${PLAN_NAME:-rabbitmq-src-daily}"
# The strategy Pod's curl reads AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from
# this Secret and trusts the S3 endpoint's CA from ca.crt in the CA Secret;
# run-all.sh materialises both from the provisioned Bucket before dispatch.
export CREDS_SECRET="${CREDS_SECRET:-rabbitmq-backup-creds}"
export CA_SECRET="${CA_SECRET:-rabbitmq-backup-ca}"
# S3 endpoint CA. cozystack's default seaweedfs serves its S3 endpoint with a
# self-signed certificate whose CA lives in this Secret; run-all.sh copies its
# ca.crt into CA_SECRET, which the strategy Pod mounts and points CURL_CA_BUNDLE
# at. The name follows the seaweedfs chart's fullnameOverride
# ("seaweedfs" -> "seaweedfs-ca-cert"); run-all.sh auto-discovers the CA
# Certificate's actual secret when this default is absent. On a cluster whose S3
# endpoint is signed by a publicly-trusted CA, set S3_CA_SECRET="" to skip the
# copy and drop the CURL_CA_BUNDLE env + CA volume from 03-rabbitmq-strategy.yaml.
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

# Wait until the rabbitmq.com/RabbitmqCluster CR reports AllReplicasReady=True
# (the cluster-operator's own readiness signal — every server pod is up and the
# management API is serving). No fail-fast value: the condition can be briefly
# absent or False during rolling startup before it converges.
wait_rabbitmq_ready() {
    local cr="$1" timeout="${2:-600}"
    wait_for_field rabbitmqclusters.rabbitmq.com "$cr" \
        '{.status.conditions[?(@.type=="AllReplicasReady")].status}' True "$NAMESPACE" "$timeout"
}

# Run rabbitmqctl inside a RabbitmqCluster's first server pod. rabbitmqctl talks
# to the local node over the Erlang dist port and needs no HTTP credentials, so
# it is the simplest way to mutate and read the DEFINITIONS (vhosts, policies)
# this demo uses as its round-trip sentinel. Args: <cr-name> <rabbitmqctl args...>
rabbitmqctl_exec() {
    local cr="$1"; shift
    kubectl -n "$NAMESPACE" exec "${cr}-server-0" -c rabbitmq -- rabbitmqctl "$@"
}

# Seed the sentinel: a vhost plus a policy on it. Both are broker DEFINITIONS
# captured by /api/definitions, so their survival through S3 proves the backup
# round-tripped the definitions (not merely that a Job ran). Args: <cr> <token>
rabbitmq_seed_sentinel() {
    local cr="$1" token="$2"
    rabbitmqctl_exec "$cr" add_vhost "$token" >/dev/null
    rabbitmqctl_exec "$cr" set_policy -p "$token" sentinel-policy '.*' \
        '{"max-length":1000}' --apply-to queues >/dev/null
}

# True (0) when the sentinel vhost exists, false (1) when it is absent. The
# exec is captured separately and a FAILED exec aborts rather than being folded
# into "absent" - otherwise a transient exec failure would read as "the sentinel
# is not there", silently passing the negative pre-checks in run-all.sh.
# Args: <cr> <token>
rabbitmq_has_sentinel() {
    local cr="$1" token="$2" out
    if ! out=$(rabbitmqctl_exec "$cr" list_vhosts name 2>/dev/null); then
        log_error "rabbitmqctl list_vhosts failed on $cr; cannot determine sentinel presence"
        exit 1
    fi
    printf '%s\n' "$out" | grep -qxF "$token"
}
