#!/bin/sh
set -euo pipefail

NAMESPACE="${NAMESPACE:-cozy-system}"
CURRENT_VERSION="${CURRENT_VERSION:-0}"
TARGET_VERSION="${TARGET_VERSION:-0}"

echo "Starting migrations from version $CURRENT_VERSION to $TARGET_VERSION"

# Check if ConfigMap exists
if ! kubectl get configmap -n "$NAMESPACE" cozystack-version >/dev/null 2>&1; then
  echo "ConfigMap cozystack-version does not exist, creating it with version $TARGET_VERSION"
  kubectl create configmap -n "$NAMESPACE" cozystack-version \
    --from-literal=version="$TARGET_VERSION" \
    --dry-run=client -o yaml | kubectl apply -f-
  echo "ConfigMap created with version $TARGET_VERSION"
  exit 0
fi

# If current version is already at target, nothing to do
if [ "$CURRENT_VERSION" -ge "$TARGET_VERSION" ]; then
  echo "Current version $CURRENT_VERSION is already at or above target version $TARGET_VERSION"
  exit 0
fi

# Run migrations sequentially from current version to target version
for i in $(seq $((CURRENT_VERSION + 1)) $TARGET_VERSION); do
  if [ -f "/migrations/$i" ]; then
    echo "Running migration $i"
    chmod +x /migrations/$i
    /migrations/$i || {
      echo "Migration $i failed"
      exit 1
    }
    echo "Migration $i completed successfully"
  else
    echo "Migration $i not found, skipping"
  fi
done

echo "All migrations completed successfully"

