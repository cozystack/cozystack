#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migrations 67 --> 68 (Redis) and 68 --> 69 (Valkey):
# convert the default password to platform-minted credentials, first part.
#
# Each migration seeds the account Secret of the default user of every release
# of its kind from <release>-auth, then creates its finish Job directly, as
# migration 62 does. These pin what the two add over 62: the seed and the Job
# take the fixed account, its source key and the enabled flag, and the Job's
# ClusterRole, ServiceAccount and binding are named for the migration, so the
# two conversions running in one upgrade share none of them.
#
# FAIL CLOSED is pinned for each path: a failed seed, a pod whose image cannot
# be read, a failed apply of the Job, and a Job that cannot be owned must stop
# the migration before it stamps the version, because migrations never re-run.
#
# The harness (docker, the migrations image base, fake kubectl and
# credentials-migrate on PATH) is the one hack/migration-58-http-cache-freeze.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-67-68-redis-valkey-credentials.bats
# -----------------------------------------------------------------------------

load test_helper

FAKEBIN="$PWD/hack/testdata/migration-67-68-redis-valkey-credentials"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration67-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-67-$$"
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
    -e FAKE_MIGRATION="$1" \
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
    echo "docker is required: these tests run migrations 67 and 68 inside a" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
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

# check_converts <migration> <kind>
check_converts() {
  prep
  rc=0
  run_migration "$1" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # The seed takes the one default user, its password from the password key
  # of <release>-auth, and authEnabled as the switch.
  grep -qxF "CREDMIGRATE seed --migration $1 --kind $2 --format plaintext --fixed-account default --source-key password --enabled-flag authEnabled --source-suffix=-auth" "$FAKE_CMDLOG"

  # The finish Job carries its least-privilege ClusterRole (never
  # cluster-admin), ServiceAccount and binding, all named for the migration
  # (five names: the three objects, the roleRef and the Job), the same account
  # flags as the seed, and the image this hook runs as.
  grep -qxE "APPLY-FINISH role=1 sa=1 crb=1 admin=0 migration=1 image=1 serviceaccount=1 fixed=5 actor=1 named=5" "$FAKE_CMDLOG"
  grep -qxE "PATCH-SA-OWNER owner=1" "$FAKE_CMDLOG"

  # Stale Job deleted before the apply, owned after it, stamped last.
  _del=$(cmdlog_line "^DELETE-JOB$")
  _apply=$(cmdlog_line "^APPLY-FINISH ")
  _own=$(cmdlog_line "^PATCH-SA-OWNER ")
  _stamp=$(cmdlog_line "^STAMP $(($1 + 1))$")
  [ -n "$_del" ] && [ "$_del" -lt "$_apply" ]
  [ -n "$_own" ] && [ "$_apply" -lt "$_own" ]
  [ -n "$_stamp" ] && [ "$_own" -lt "$_stamp" ]
  rm -rf "$WORK"
}

# check_fails_closed <migration> <fake variable> <value>
check_fails_closed() {
  prep
  export "$2=$3"
  rc=0
  run_migration "$1" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "migration $1 stamped after $2" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "67 converts Redis: seeds, creates its own finish Job, and stamps 68" {
  check_converts 67 Redis
}

@test "68 converts Valkey: seeds, creates its own finish Job, and stamps 69" {
  check_converts 68 Valkey
}

@test "a failed seed stops before the Job and the stamp" {
  for n in 67 68; do
    check_fails_closed "$n" FAKE_SEED_FAIL "apiserver is unavailable"
  done
}

@test "a pod whose image cannot be read stops before the Job and the stamp" {
  for n in 67 68; do
    check_fails_closed "$n" FAKE_GETPOD_FAIL "the server could not find the requested resource"
    check_fails_closed "$n" FAKE_IMAGE_EMPTY 1
  done
}

@test "a failed apply of the Job stops before stamping" {
  for n in 67 68; do
    check_fails_closed "$n" FAKE_APPLY_FAIL "admission webhook denied the request"
  done
}

@test "a Job that cannot be owned stops before stamping" {
  for n in 67 68; do
    check_fails_closed "$n" FAKE_GETJOB_FAIL "the server was unable to return a response"
    check_fails_closed "$n" FAKE_JOBUID_EMPTY 1
    check_fails_closed "$n" FAKE_PATCH_FAIL "serviceaccounts not found"
  done
}
