#!/bin/bash
# Run every numbered step in order. Stops on the first failure.
# Set SKIP_RESTORE=1 to stop after a successful backup (steps 01-02).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

steps=(01 02 03 04)
[[ "${SKIP_RESTORE:-0}" == "1" ]] && steps=(01 02)

for step in "${steps[@]}"; do
    # find is glob-safe under set -euo pipefail and degrades to empty output
    # when no file matches, unlike `ls glob | head`.
    script="$(find "$SCRIPT_DIR" -maxdepth 1 -type f -name "${step}-*.sh" | sort | head -n1)"
    if [[ -z "$script" ]]; then
        echo "no script found for step $step" >&2
        exit 1
    fi
    echo "=== Running $(basename "$script") ==="
    bash "$script"
done

[[ "${SKIP_RESTORE:-0}" == "1" ]] && echo "SKIP_RESTORE=1: stopping after a successful backup"
