#!/bin/bash
# Idempotent teardown for the S3 Bucket backup demo. Safe to run repeatedly and
# on a partially-applied state (--ignore-not-found throughout).
set -eu

DIR="$(cd "$(dirname "$0")" && pwd)"
source "$DIR/00-helpers.sh"

k delete restorejob "$RESTOREJOB_INPLACE_NAME" "$RESTOREJOB_TOCOPY_NAME" --ignore-not-found
k delete backupjob "$BACKUPJOB_NAME" --ignore-not-found
k delete backup "$BACKUPJOB_NAME" --ignore-not-found
k delete plan "$PLAN_NAME" --ignore-not-found
kubectl delete backupclass "$BACKUPCLASS_NAME" --ignore-not-found
kubectl delete bucket.strategy.backups.cozystack.io "$STRATEGY_NAME" --ignore-not-found
# BucketAccesses the driver provisioned (no ownerRef, so not GC'd with the apps).
k delete bucketaccess "${SRC_RELEASE}-cozy-backup" "${TGT_RELEASE}-cozy-restore" --ignore-not-found
k delete secret "$DEST_CREDS_SECRET" --ignore-not-found
k delete bucket.apps.cozystack.io "$SRC_APP" "$DST_APP" "$TGT_APP" --ignore-not-found
