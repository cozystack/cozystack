#!/bin/sh
set -euo pipefail

# shellcheck source=migrations/lib/cozystack-version.sh
. /migrations/lib/cozystack-version.sh

NAMESPACE="${NAMESPACE:-cozy-system}"
CURRENT_VERSION="${CURRENT_VERSION:-0}"
TARGET_VERSION="${TARGET_VERSION:-0}"

echo "Starting migrations from version $CURRENT_VERSION to $TARGET_VERSION"

# Check if ConfigMap exists
if ! kubectl get configmap --namespace "$NAMESPACE" cozystack-version >/dev/null 2>&1; then
  echo "ConfigMap cozystack-version does not exist, creating it with version $TARGET_VERSION"
  # Stamp via the shared helper so the bootstrap ConfigMap carries the
  # platform.cozystack.io/no-delete label, matching every go-forward stamp
  # (migration 42 onward; migrations 1-41 stamp label-less and are backfilled
  # by migration 42, so they are intentionally left as-is).
  stamp_cozystack_version "$TARGET_VERSION"
  echo "ConfigMap created with version $TARGET_VERSION"
  exit 0
fi

# If current version is already at target, nothing to do
if [ "$CURRENT_VERSION" -ge "$TARGET_VERSION" ]; then
  echo "Current version $CURRENT_VERSION is already at or above target version $TARGET_VERSION"
  exit 0
fi

# Run migrations sequentially from current version to target version.
# Every version below TARGET_VERSION must have a corresponding migration file
# baked into the image — the release build rebuilds the migrations image from
# the same source tree that carries the files and restamps the digest in the
# same commit, so image and targetVersion are always consistent at release
# time. A missing file therefore indicates a packaging mistake (digest
# advanced without the file landing) and must fail loudly rather than stall
# the cluster at a stale version.
for i in $(seq $CURRENT_VERSION $((TARGET_VERSION - 1))); do
  if [ ! -f "/migrations/$i" ]; then
    echo "Migration $i not found in image — refusing to advance past a missing migration" >&2
    exit 1
  fi
  echo "Running migration $i"
  chmod +x /migrations/$i
  /migrations/$i || {
    echo "Migration $i failed"
    exit 1
  }
  echo "Migration $i completed successfully"
done

echo "All migrations completed successfully"
