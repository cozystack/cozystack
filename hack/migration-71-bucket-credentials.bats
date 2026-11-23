#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 71 --> 72 (convert Bucket users to
# platform-minted credentials).
#
# The migration marks every tenant BucketAccess the bucket chart rendered with
# helm.sh/resource-policy=keep and credentials.cozystack.io/adopt=true, so the
# chart upgrade leaves it in place and cozystack-controller adopts it with its
# keys, and marks the backup bucket's system user platform. See
# packages/core/platform/images/migrations/migrations/71.
#
# FAIL CLOSED is pinned: a failed listing or a failed mark must stop the
# migration before it stamps the version, because migrations never re-run.
#
# The harness (docker, the migrations image base, fake kubectl on PATH) is the
# one hack/migration-58-http-cache-freeze.bats describes.
#
# Run with: hack/cozytest.sh hack/migration-71-bucket-credentials.bats
# -----------------------------------------------------------------------------

load test_helper

FAKEBIN="$PWD/hack/testdata/migration-71-bucket-credentials"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration71-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-71-$$"

ACCESSES='tenant-foo bucket-photos-web bucket-photos 
tenant-foo bucket-photos-reader bucket-photos 
tenant-foo bucket-photos-public-read bucket-photos 
tenant-root bucket-cozy-backups-system bucket-cozy-backups 
tenant-foo bucket-done-app bucket-done CredentialTemplate
tenant-foo unrelated some-release '

cmdlog_line() {
  grep -nE -- "$1" "$FAKE_CMDLOG" | head -1 | cut -d: -f1
}

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

run_migration() {
  _run_migration_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$MIG_DIR:/migrations:ro" \
    -v "$FAKEBIN:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_CMDLOG=/work/cmdlog \
    -e NAMESPACE=cozy-system \
    -e FAKE_ACCESSES="${FAKE_ACCESSES-}" \
    -e FAKE_BACKUP_NS="${FAKE_BACKUP_NS-}" \
    -e FAKE_BACKUP_BUCKET="${FAKE_BACKUP_BUCKET-}" \
    -e FAKE_NO_SYSTEM="${FAKE_NO_SYSTEM-}" \
    -e FAKE_NO_CRD="${FAKE_NO_CRD-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_LABEL_FAIL="${FAKE_LABEL_FAIL-}" \
    -e FAKE_KEYLESS="${FAKE_KEYLESS-}" \
    -e FAKE_STRIP_FAIL="${FAKE_STRIP_FAIL-}" \
    -e FAKE_CRD_FAIL="${FAKE_CRD_FAIL-}" \
    -e CREDENTIAL_CRD_FILE=/work/credentialtemplates.yaml \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 71 inside a build of" >&2
    echo "$ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export FAKE_ACCESSES="$ACCESSES"
  unset FAKE_BACKUP_NS FAKE_BACKUP_BUCKET FAKE_NO_SYSTEM FAKE_NO_CRD FAKE_LIST_FAIL FAKE_LABEL_FAIL FAKE_CRD_FAIL FAKE_KEYLESS FAKE_STRIP_FAIL || true
  return 0
}

@test "it marks tenant BucketAccesses, the backup user platform, and stamps" {
  prep
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  grep -qxF "PATCH-PLATFORM tenant-root/bucket-cozy-backups platform=1" "$FAKE_CMDLOG"
  # The CRD with spec.bucketAccess is applied, owned by the
  # cozystack-controller release, and established before anything else.
  _crd=$(cmdlog_line "^CRD established$")
  _platform=$(cmdlog_line "^PATCH-PLATFORM ")
  [ -n "$_crd" ] && [ "$_crd" -lt "$_platform" ]
  grep -qxF "CRD label" "$FAKE_CMDLOG"
  grep -qxF "CRD annotate" "$FAKE_CMDLOG"
  for name in bucket-photos-web bucket-photos-reader; do
    grep -qxF "KEEP tenant-foo/$name keep=1" "$FAKE_CMDLOG"
    grep -qxF "ADOPT tenant-foo/$name adopt=1" "$FAKE_CMDLOG"
  done
  # public-read has no keys, the backup user stays a platform account, an
  # adopted BucketAccess is left alone, and nothing a bucket chart did not
  # render is touched.
  for name in bucket-photos-public-read bucket-cozy-backups-system bucket-done-app unrelated; do
    if grep -qE "^(KEEP|ADOPT) [^ ]*/$name " "$FAKE_CMDLOG"; then echo "marked $name" >&2; return 1; fi
  done
  # Each tenant user's readable Secret loses its keys; the platform user's
  # keeps them, and nothing reads the key values out.
  for name in bucket-photos-web bucket-photos-reader bucket-done-app; do
    grep -qxF "STRIP tenant-foo/$name-credentials strip=1" "$FAKE_CMDLOG"
  done
  if grep -qE "^STRIP [^ ]*/bucket-cozy-backups-system-credentials " "$FAKE_CMDLOG"; then echo "stripped the platform user's keys" >&2; return 1; fi
  if grep -qE "^READ-KEYS " "$FAKE_CMDLOG"; then echo "read the key values" >&2; return 1; fi
  _strip=$(cmdlog_line "^STRIP ")
  _stamp=$(cmdlog_line "^STAMP 72$")
  [ -n "$_strip" ] && [ "$_strip" -lt "$_stamp" ]
  rm -rf "$WORK"
}

@test "a Secret that holds no keys is not patched" {
  prep
  export FAKE_ACCESSES='tenant-foo bucket-photos-web bucket-photos '
  export FAKE_KEYLESS=bucket-photos-web-credentials
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^STRIP " "$FAKE_CMDLOG"; then echo "patched a keyless Secret" >&2; return 1; fi
  grep -qxF "ADOPT tenant-foo/bucket-photos-web adopt=1" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed strip stops before stamping" {
  prep
  export FAKE_STRIP_FAIL="secrets is forbidden"
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed strip" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "it finds the backup bucket where backupstrategy-controller provisions it" {
  prep
  export FAKE_BACKUP_NS=tenant-ops FAKE_BACKUP_BUCKET=backups
  export FAKE_ACCESSES='tenant-ops bucket-backups-system bucket-backups '
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qxF "PATCH-PLATFORM tenant-ops/bucket-backups platform=1" "$FAKE_CMDLOG"
  if grep -qE "^(KEEP|ADOPT) " "$FAKE_CMDLOG"; then echo "marked the backup user" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a backup release without a system user is not given one" {
  prep
  export FAKE_NO_SYSTEM=1
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^PATCH-PLATFORM" "$FAKE_CMDLOG"; then echo "patched a release without a system user" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a cluster without the COSI API stamps without listing" {
  prep
  export FAKE_NO_CRD=1
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qF "get bucketaccesses" "$FAKE_CMDLOG"; then echo "listed BucketAccesses without the CRD" >&2; return 1; fi
  grep -qxF "STAMP 72" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed listing stops before stamping" {
  prep
  export FAKE_LIST_FAIL="the server was unable to return a response"
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed listing" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed mark stops before stamping" {
  prep
  export FAKE_LABEL_FAIL="admission webhook denied the request"
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed mark" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a CRD that fails to apply stops before any mark and the stamp" {
  prep
  export FAKE_CRD_FAIL=1
  rc=0
  run_migration 71 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^(PATCH-PLATFORM|KEEP|ADOPT) " "$FAKE_CMDLOG"; then echo "marked without the CRD" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped without the CRD" >&2; return 1; fi
  rm -rf "$WORK"
}
