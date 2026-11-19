#!/bin/bash
# Convenience runner: executes 01..05 in order. Useful for smoke
# testing the demo on a fresh cluster. Stops on the first failure.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"$SCRIPT_DIR/01-create-strategy.sh"
"$SCRIPT_DIR/02-create-bucket.sh"
"$SCRIPT_DIR/03-create-etcd-src.sh"
"$SCRIPT_DIR/04-create-backupjob.sh"
"$SCRIPT_DIR/05-restore-in-place.sh"
