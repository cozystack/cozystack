#!/bin/bash
# End-to-end tenant Kubernetes backup/restore walkthrough. It proves the
# restore brings back the same cluster: the CA fingerprint is unchanged, and a
# marker kept in etcd and one kept on a PersistentVolume both come back, first
# after rolling a live cluster back, then after rebuilding a deleted one.
#
# Needs the platform default backups (backupstrategy-controller with a Velero
# BSL and the system bucket), so cozy-default routes Kubernetes to
# cozy-default-kubernetes.
#
# Env knobs: see 00-helpers.sh. SKIP_RESTORE=1 stops after the backup.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

kubectl wait --for=create kubernetes.strategy.backups.cozystack.io/cozy-default-kubernetes --timeout=15m >/dev/null \
    || { log_error "cozy-default-kubernetes strategy is missing: enable the platform default backups first"; exit 1; }

steps=(01 02 03 04)
[[ "${SKIP_RESTORE:-0}" == "1" ]] && steps=(01 02)
for step in "${steps[@]}"; do
    script="$(find "$SCRIPT_DIR" -maxdepth 1 -type f -name "${step}-*.sh" | sort | head -n1)"
    [[ -n "$script" ]] || { echo "no script found for step $step" >&2; exit 1; }
    echo "=== Running $(basename "$script") ==="
    bash "$script"
done

print_header "Walkthrough complete"
