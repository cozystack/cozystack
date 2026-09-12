#!/bin/bash
# Honest S3 Bucket backup/restore round-trip. Proves data integrity, not
# liveness: it writes a sentinel object into the source bucket, backs it up,
# deletes it, restores in-place, and asserts the exact bytes round-tripped
# through object storage - then repeats the assertion for a to-copy restore
# into a separate bucket. Fail-fast, no retries on deterministic steps.
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
source "$DIR/00-helpers.sh"

step() { echo -e "\n${BLUE}==> $*${NC}"; }

step "Provision source and destination buckets"
k apply -f "$DIR/00-bucket-src.yaml"
k apply -f "$DIR/01-bucket-dest.yaml"
wait_hr_ready "${SRC_RELEASE}-system" 300
wait_hr_ready "${DST_RELEASE}-system" 300
wait_field bucketclaim "$SRC_RELEASE" '{.status.bucketReady}' true 180
wait_field bucketclaim "$DST_RELEASE" '{.status.bucketReady}' true 180
wait_field bucketaccess "${DST_RELEASE}-backup" '{.status.accessGranted}' true 120

SRC_BUCKET="$(k get bucketclaim "$SRC_RELEASE" -o jsonpath='{.status.bucketName}')"
DST_BUCKET="$(k get bucketclaim "$DST_RELEASE" -o jsonpath='{.status.bucketName}')"
[ -n "$SRC_BUCKET" ] && [ -n "$DST_BUCKET" ] || { echo -e "${RED}bucket names unresolved${NC}" >&2; exit 1; }

step "Project destination credentials into $DEST_CREDS_SECRET"
DST_AK="$(bucketinfo_field "${DST_RELEASE}-backup" '.spec.secretS3.accessKeyID')"
DST_SK="$(bucketinfo_field "${DST_RELEASE}-backup" '.spec.secretS3.accessSecretKey')"
k create secret generic "$DEST_CREDS_SECRET" \
  --from-literal=AWS_ACCESS_KEY_ID="$DST_AK" \
  --from-literal=AWS_SECRET_ACCESS_KEY="$DST_SK" \
  --dry-run=client -o yaml | k apply -f -

step "Install test-scoped strategy + BackupClass + Plan"
CONTROLLER_IMAGE="$(controller_image)"
[ -n "$CONTROLLER_IMAGE" ] || { echo -e "${RED}could not discover backupstrategy-controller image${NC}" >&2; exit 1; }
sed -e "s|REPLACE_WITH_DEST_BUCKET|$DST_BUCKET|" \
    -e "s|REPLACE_WITH_CONTROLLER_IMAGE|$CONTROLLER_IMAGE|" \
    "$DIR/10-bucket-strategy.yaml" | kubectl apply -f -
kubectl apply -f "$DIR/15-backupclass.yaml"
k apply -f "$DIR/20-plan.yaml"

# Port-forward the in-cluster S3 endpoint for the sentinel client; the trap is
# self-contained inside this script (the only EXIT trap the e2e conventions
# allow).
step "Open S3 port-forward and seed the sentinel"
kubectl -n "$NAMESPACE" port-forward "service/$S3_SVC" "$S3_PORT:$S3_PORT" >/dev/null 2>&1 &
PF_PID=$!
trap '[ -n "${PF_PID:-}" ] && kill "$PF_PID" 2>/dev/null || true' EXIT
timeout 30 sh -ec "until nc -z localhost $S3_PORT; do sleep 1; done"

SEED_AK="$(bucketinfo_field "${SRC_RELEASE}-seed" '.spec.secretS3.accessKeyID')"
SEED_SK="$(bucketinfo_field "${SRC_RELEASE}-seed" '.spec.secretS3.accessSecretKey')"
mc alias set src "https://localhost:$S3_PORT" "$SEED_AK" "$SEED_SK" --insecure
printf '%s' "$SENTINEL_VALUE" | mc pipe --insecure "src/$SRC_BUCKET/$SENTINEL_KEY"
echo "seeded src/$SRC_BUCKET/$SENTINEL_KEY = $SENTINEL_VALUE"

step "Run BackupJob and wait for the Backup artifact"
k apply -f "$DIR/25-backupjob-adhoc.yaml"
wait_field backupjob "$BACKUPJOB_NAME" '{.status.phase}' Succeeded 480
k get backup "$BACKUPJOB_NAME" >/dev/null || { echo -e "${RED}Backup $BACKUPJOB_NAME not created${NC}" >&2; exit 1; }

step "Drop the sentinel from the source, then restore in-place"
mc rm --insecure "src/$SRC_BUCKET/$SENTINEL_KEY"
k apply -f "$DIR/35-restorejob-in-place.yaml"
wait_field restorejob "$RESTOREJOB_INPLACE_NAME" '{.status.phase}' Succeeded 480
GOT="$(mc cat --insecure "src/$SRC_BUCKET/$SENTINEL_KEY")"
[ "$GOT" = "$SENTINEL_VALUE" ] || { echo -e "${RED}in-place restore mismatch: got '$GOT'${NC}" >&2; exit 1; }
echo -e "${GREEN}in-place restore round-tripped the sentinel${NC}"

step "Provision the to-copy target and restore into it"
k apply -f "$DIR/30-bucket-target.yaml"
wait_hr_ready "${TGT_RELEASE}-system" 300
wait_field bucketclaim "$TGT_RELEASE" '{.status.bucketReady}' true 180
k apply -f "$DIR/40-restorejob-to-copy.yaml"
wait_field restorejob "$RESTOREJOB_TOCOPY_NAME" '{.status.phase}' Succeeded 480
# The driver provisioned bucket-<tgt>-cozy-restore on the target; read the
# sentinel back through it.
wait_field bucketaccess "${TGT_RELEASE}-cozy-restore" '{.status.accessGranted}' true 120
TGT_BUCKET="$(k get bucketclaim "$TGT_RELEASE" -o jsonpath='{.status.bucketName}')"
TGT_AK="$(bucketinfo_field "${TGT_RELEASE}-cozy-restore" '.spec.secretS3.accessKeyID')"
TGT_SK="$(bucketinfo_field "${TGT_RELEASE}-cozy-restore" '.spec.secretS3.accessSecretKey')"
mc alias set tgt "https://localhost:$S3_PORT" "$TGT_AK" "$TGT_SK" --insecure
GOT_COPY="$(mc cat --insecure "tgt/$TGT_BUCKET/$SENTINEL_KEY")"
[ "$GOT_COPY" = "$SENTINEL_VALUE" ] || { echo -e "${RED}to-copy restore mismatch: got '$GOT_COPY'${NC}" >&2; exit 1; }
echo -e "${GREEN}to-copy restore round-tripped the sentinel${NC}"

echo -e "\n${GREEN}Bucket backup/restore round-trip PASSED${NC}"
