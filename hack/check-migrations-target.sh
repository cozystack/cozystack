#!/usr/bin/env bash
# Verify packages/core/platform/values.yaml `migrations.targetVersion`
# is at least 1 greater than the highest-numbered file in
# packages/core/platform/images/migrations/migrations/.
#
# Catches the regression where a PR adds a new migration script but
# forgets to bump targetVersion: run-migrations.sh loops
# `seq $CURRENT_VERSION $((TARGET_VERSION - 1))`, so migration N never
# runs unless targetVersion is >= N+1.
set -euo pipefail

MDIR="packages/core/platform/images/migrations/migrations"
VALUES="packages/core/platform/values.yaml"

if [ ! -d "$MDIR" ]; then
  echo "ERROR: migrations dir not found: $MDIR" >&2
  exit 1
fi
if [ ! -f "$VALUES" ]; then
  echo "ERROR: values file not found: $VALUES" >&2
  exit 1
fi

MAX_N=0
for f in "$MDIR"/*; do
  name=${f##*/}
  [[ "$name" =~ ^[0-9]+$ ]] || continue
  [ "$name" -gt "$MAX_N" ] && MAX_N=$name
done
if [ "$MAX_N" -eq 0 ]; then
  echo "ERROR: no numbered migrations found under $MDIR" >&2
  exit 1
fi
REQUIRED=$((MAX_N + 1))

TARGET=$(yq -r '.migrations.targetVersion' "$VALUES")
if [ -z "$TARGET" ] || [ "$TARGET" = "null" ]; then
  echo "ERROR: migrations.targetVersion is not set in $VALUES" >&2
  exit 1
fi

if [ "$TARGET" -lt "$REQUIRED" ]; then
  cat >&2 <<EOF
ERROR: latest migration is $MAX_N but $VALUES sets migrations.targetVersion=$TARGET (need >= $REQUIRED).

run-migrations.sh loops 'seq \$CURRENT_VERSION \$((TARGET_VERSION - 1))', so migration $MAX_N never runs unless targetVersion is bumped.

Fix: edit $VALUES and set migrations.targetVersion: $REQUIRED
EOF
  exit 1
fi

echo "OK: migrations.targetVersion=$TARGET >= $REQUIRED (latest migration $MAX_N)"
