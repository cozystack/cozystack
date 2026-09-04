#!/bin/bash
# Run every numbered step in order. Stops on the first failure.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

for step in 01 02 03 04 05 06 07; do
    # find is glob-safe under set -euo pipefail and degrades to empty output
    # when no file matches, unlike `ls glob | head` which mixes shell
    # expansion semantics with `ls` exit codes.
    script="$(find "$SCRIPT_DIR" -maxdepth 1 -type f -name "${step}-*.sh" | sort | head -n1)"
    if [[ -z "$script" ]]; then
        echo "no script found for step $step" >&2
        exit 1
    fi
    echo "=== Running $(basename "$script") ==="
    bash "$script"
done
