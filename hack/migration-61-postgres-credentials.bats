#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 61 --> 62 (convert Postgres accounts to
# platform-minted credentials, first part).
#
# The migration seeds the account Secrets of every Postgres release, then
# creates the cozystack-credentials-migration-61 finish Job directly (rather
# than rendering it from the platform chart) so a changing migrations image
# never collides with an immutable pod template and helm-controller waits on no
# long-running Job. See packages/core/platform/images/migrations/migrations/61.
#
# FAIL CLOSED is pinned for each path: a failed seed, a pod whose image cannot
# be read, and a failed apply of the Job must stop the migration before it
# stamps the version, because migrations never re-run.
#
# The harness (docker, the migrations image base, fake kubectl and
# credentials-migrate on PATH) is the one hack/migration-58-http-cache-freeze.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-61-postgres-credentials.bats
# -----------------------------------------------------------------------------

load test_helper

FAKEBIN="$PWD/hack/testdata/migration-61-postgres-credentials"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration61-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-61-$$"
IMAGE=registry.example.org/cozystack/cozystack:v1.7.0

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

cmdlog_line() {
  grep -nE -- "$1" "$FAKE_CMDLOG" | head -1 | cut -d: -f1
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
    -e NAMESPACE="${NAMESPACE-}" \
    -e FAKE_IMAGE="${FAKE_IMAGE-}" \
    -e FAKE_IMAGE_EMPTY="${FAKE_IMAGE_EMPTY-}" \
    -e FAKE_GETPOD_FAIL="${FAKE_GETPOD_FAIL-}" \
    -e FAKE_APPLY_FAIL="${FAKE_APPLY_FAIL-}" \
    -e FAKE_SEED_FAIL="${FAKE_SEED_FAIL-}" \
    -e FAKE_GETJOB_FAIL="${FAKE_GETJOB_FAIL-}" \
    -e FAKE_JOBUID_EMPTY="${FAKE_JOBUID_EMPTY-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 61 inside a build of" >&2
    echo "$ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl" "$FAKEBIN/credentials-migrate"
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_IMAGE="$IMAGE"
  unset FAKE_IMAGE_EMPTY FAKE_GETPOD_FAIL FAKE_APPLY_FAIL FAKE_SEED_FAIL FAKE_GETJOB_FAIL FAKE_JOBUID_EMPTY FAKE_PATCH_FAIL || true
  return 0
}

@test "it seeds, creates the finish Job on its own image, and stamps" {
  prep
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # The seed runs for migration 61.
  grep -qE "^CREDMIGRATE seed --migration 61 " "$FAKE_CMDLOG"

  # The finish Job carries its least-privilege ClusterRole (never cluster-admin),
  # ServiceAccount and binding, the migration number, and the image this hook
  # runs as, read from its own pod rather than values.
  grep -qE "^APPLY-FINISH role=1 sa=1 crb=1 admin=0 jobname=1 migration=1 image=1 serviceaccount=1$" "$FAKE_CMDLOG"

  # The Job owns its ServiceAccount, so the cluster-admin identity is collected
  # with the Job and does not stand indefinitely.
  grep -qE "^PATCH-SA-OWNER owner=1$" "$FAKE_CMDLOG"

  # A stale Job from an earlier run is deleted before the apply, so a rerun on a
  # newer image does not fault on the immutable pod template; the ownership is
  # set after the Job exists; and only then, after the Job is created and owned,
  # the version is stamped. A stamp before the Job would mark the migration done
  # with no finisher.
  _del=$(cmdlog_line "^DELETE-JOB$")
  _apply=$(cmdlog_line "^APPLY-FINISH ")
  _own=$(cmdlog_line "^PATCH-SA-OWNER ")
  _stamp=$(cmdlog_line "^STAMP 62$")
  [ -n "$_del" ] && [ "$_del" -lt "$_apply" ]
  [ -n "$_own" ] && [ "$_apply" -lt "$_own" ]
  [ -n "$_stamp" ] && [ "$_own" -lt "$_stamp" ]
  rm -rf "$WORK"
}

@test "a pod whose image cannot be read, as a failed get, stops before the Job and the stamp" {
  prep
  export FAKE_GETPOD_FAIL="the server could not find the requested resource"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qE "^CREDMIGRATE seed --migration 61 " "$FAKE_CMDLOG"
  if grep -qE "^APPLY-FINISH" "$FAKE_CMDLOG"; then echo "created the Job after a failed image read" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed image read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed seed stops before the Job and before stamping" {
  prep
  export FAKE_SEED_FAIL="apiserver is unavailable"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^APPLY-FINISH" "$FAKE_CMDLOG"; then echo "created the Job after a failed seed" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed seed" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a pod whose image cannot be read stops after the seed, before the Job and the stamp" {
  prep
  export FAKE_IMAGE_EMPTY=1
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qE "^CREDMIGRATE seed --migration 61 " "$FAKE_CMDLOG"
  if grep -qE "^APPLY-FINISH" "$FAKE_CMDLOG"; then echo "created the Job with no image to run" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped with no image to run" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed apply of the Job stops before stamping" {
  prep
  export FAKE_APPLY_FAIL="admission webhook denied the request"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after the Job failed to apply" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a Job whose uid cannot be read stops before owning the SA and before stamping" {
  prep
  export FAKE_GETJOB_FAIL="the server was unable to return a response"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^PATCH-SA-OWNER" "$FAKE_CMDLOG"; then echo "patched ownership with no uid" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped without owning the SA" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "an empty Job uid stops before owning the SA and before stamping" {
  prep
  export FAKE_JOBUID_EMPTY=1
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^PATCH-SA-OWNER" "$FAKE_CMDLOG"; then echo "patched ownership with an empty uid" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped with an empty uid" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed ownership patch stops before stamping" {
  prep
  export FAKE_PATCH_FAIL="serviceaccounts \"cozystack-credentials-migration\" not found"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after the ownership patch failed" >&2; return 1; fi
  rm -rf "$WORK"
}
