#!/bin/sh
# Cleanup leftover app resources in tenant-test namespace before retrying an E2E test.
# Usage: e2e-cleanup-app.sh <app-name>
# Example: e2e-cleanup-app.sh qdrant

set -e

APP="$1"
if [ -z "$APP" ]; then
	echo "Usage: $0 <app-name>" >&2
	exit 1
fi

NS="tenant-test"

echo "=== Cleaning up leftover resources for app '$APP' in namespace '$NS' ==="

# Delete the custom resource(s) by guessing the Kind from app name
# Each bats test creates a resource matching the app name; delete all CR types
kubectl api-resources --verbs=list --namespaced -o name 2>/dev/null |
	grep '\.apps\.cozystack\.io' |
	while read -r resource; do
		kubectl -n "$NS" delete "$resource" --all --ignore-not-found --wait=false 2>/dev/null || true
	done

# Remove all HelmReleases matching the app prefix
kubectl -n "$NS" get hr --no-headers -o custom-columns=':metadata.name' 2>/dev/null |
	grep "^${APP}-" |
	while read -r hr; do
		echo "Deleting HelmRelease $hr"
		kubectl -n "$NS" delete hr "$hr" --ignore-not-found --wait=false 2>/dev/null || true
	done

# Give controllers a moment to process deletions
sleep 10

echo "=== Cleanup done ==="
