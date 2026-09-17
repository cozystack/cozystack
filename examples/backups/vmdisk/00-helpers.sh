#!/bin/bash
# Helper functions and variables for the VMDisk backup/restore demo.
# Source this file in other scripts: source "$(dirname "$0")/00-helpers.sh"

# ANSI color codes
export RED='\033[0;31m'
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export MAGENTA='\033[0;35m'
export CYAN='\033[0;36m'
export WHITE='\033[1;37m'
export NC='\033[0m'
export BOLD='\033[1m'

# Default settings
export NAMESPACE="${NAMESPACE:-tenant-root}"
# The platform ships the SeaweedFS-backed BackupStorageLocation as cozy-default
# (packages/system/backupstrategy-controller). Point the demo/e2e strategy at it
# by default so the flow works out of the box on a backups-enabled cluster;
# override for an external S3 whose BSL carries a different name.
export BACKUP_STORAGE_LOCATION="${BACKUP_STORAGE_LOCATION:-cozy-default}"

# Logging functions (output to stderr to avoid polluting captured output)
log_info()    { echo -e "${CYAN}ℹ${NC} $*" >&2; }
log_success() { echo -e "${GREEN}✔${NC} $*" >&2; }
log_warning() { echo -e "${YELLOW}⚠${NC} $*" >&2; }
log_error()   { echo -e "${RED}✖${NC} $*" >&2; }
log_step()    { echo -e "\n${MAGENTA}${BOLD}▶ $*${NC}" >&2; }
log_command() { echo -e "${WHITE}  \$ $*${NC}" >&2; }
log_substep() { echo -e "${CYAN}  → $*${NC}" >&2; }

separator() {
    echo -e "\n${CYAN}────────────────────────────────────────────────────────────${NC}\n" >&2
}

print_header() {
    local title="$1"
    echo -e "\n${MAGENTA}${BOLD}== ${title} ==${NC}\n" >&2
}

# wait_for_field, wait_hr_ready and wait_deleted live in one file shared by
# every backup walkthrough, so a fix to one reaches all of them.
# shellcheck source-path=SCRIPTDIR source=../_lib/wait-helpers.sh
source "$(dirname "${BASH_SOURCE[0]}")/../_lib/wait-helpers.sh"
