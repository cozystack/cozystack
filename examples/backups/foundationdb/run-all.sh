#!/bin/bash
# Convenience runner that executes 01..06 in order.
#
# Note: cleanup.sh is intentionally NOT chained here so the operator
# can inspect the resulting BackupClass/Backup/RestoreJob state after
# the demo. Run cleanup.sh manually when finished.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

bash 01-create-strategy.sh
bash 02-create-bucket.sh
bash 03-create-foundationdb-src.sh
bash 04-create-backupjob.sh
bash 05-restore-in-place.sh
bash 06-restore-to-copy.sh
