#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 59 --> 60 (pin
# gateway.passthroughMode=separateAddress on a cluster that already runs the
# Gateway, so the upgrade does not move its tenant API to :6443).
#
# Four properties are pinned here:
#
#  1. ONLY A GATEWAY INSTALL WITH NO MODE. gateway.enabled true, as a bool or as
#     the string the Package's values JSON may carry, with no passthroughMode,
#     gets exactly the separateAddress patch. A set mode, either one, is left
#     alone, and so are a Gateway that is off and a Package that is not there.
#
#  2. EXACTLY THIS PATCH, on the platform Package, as a merge patch.
#
#  3. FAIL CLOSED. A failed read or a failed patch aborts before the version
#     stamp: migrations never re-run, and a swallowed error would stamp past a
#     cluster that then renders singleAddress.
#
#  4. EVERY PATH STAMPS 60, and only once.
#
# These drive the real migration script end-to-end against a fake kubectl
# (hack/testdata/migration-59-passthrough-mode/), mocking only the cluster
# boundary.
#
# SHELL. Production runs the migration under /bin/sh = busybox ash, by path, so
# run_migration() runs it by path inside the migrations image's own pinned base
# with jq added, --network none. See hack/migration-54-redis-adopt.bats for the
# full reasoning; it applies unchanged.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line, rewriting the latter into `return 0` + `}`. A helper whose exit status
# matters must capture it and `return` it by hand before its closing brace.
#
# Run with: hack/cozytest.sh hack/migration-59-passthrough-mode.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-59-passthrough-mode"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
PIN='{"spec":{"components":{"platform":{"values":{"gateway":{"passthroughMode":"separateAddress"}}}}}}'

ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)

TESTIMG="cozystack-migration59-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"

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
    -e FAKE_VALUES="${FAKE_VALUES-}" \
    -e FAKE_GET_FAIL="${FAKE_GET_FAIL-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    "$TESTIMG" /migrations/59 || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 59 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d "${TMPDIR:-/tmp}/mig59.XXXXXX")
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export FAKE_VALUES="-"
  unset FAKE_GET_FAIL FAKE_PATCH_FAIL || true
  return 0
}

# stamped_once -- the run stamped 60 exactly once and nothing else.
stamped_once() {
  [ "$(grep -cE '^STAMP ' "$FAKE_CMDLOG")" -eq 1 ] && grep -qxF -- "STAMP 60" "$FAKE_CMDLOG"
  return $?
}

# --- 1, 2 and 4. who is pinned, with what --------------------------------------

@test "a Gateway install with no mode is pinned to separateAddress" {
  prep
  for values in '{"gateway":{"enabled":true}}' '{"gateway":{"enabled":"true"}}' '{"gateway":{"enabled":true,"passthroughMode":null}}' '{"gateway":{"enabled":true,"passthroughMode":""}}'; do
    : > "$FAKE_CMDLOG"
    export FAKE_VALUES="$values"
    rc=0
    run_migration >"$WORK/out" 2>&1 || rc=$?
    cat "$WORK/out"; cat "$FAKE_CMDLOG"
    [ "$rc" -eq 0 ]
    [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 1 ]
    grep -qxF -- "PATCH $PIN" "$FAKE_CMDLOG"
    stamped_once
  done
  rm -rf "$WORK"
}

@test "a mode already set, a Gateway that is off and a missing Package are left alone" {
  prep
  for values in '{"gateway":{"enabled":true,"passthroughMode":"singleAddress"}}' '{"gateway":{"enabled":true,"passthroughMode":"separateAddress"}}' '{"gateway":{"enabled":false}}' '{"gateway":{}}' '{}' '-'; do
    : > "$FAKE_CMDLOG"
    export FAKE_VALUES="$values"
    rc=0
    run_migration >"$WORK/out" 2>&1 || rc=$?
    cat "$WORK/out"; cat "$FAKE_CMDLOG"
    [ "$rc" -eq 0 ]
    [ "$(grep -cE '^PATCH ' "$FAKE_CMDLOG")" -eq 0 ]
    [ "$(grep -c '^UNHANDLED ' "$FAKE_CMDLOG")" -eq 0 ]
    stamped_once
  done
  rm -rf "$WORK"
}

# --- 3. fail closed -------------------------------------------------------------

@test "a failed read of the Package aborts before stamping" {
  prep
  export FAKE_VALUES='{"gateway":{"enabled":true}}'
  export FAKE_GET_FAIL=1
  rc=0
  run_migration >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -cE '^(PATCH|STAMP) ' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

@test "a failed patch aborts before stamping" {
  prep
  export FAKE_VALUES='{"gateway":{"enabled":true}}'
  export FAKE_PATCH_FAIL=1
  rc=0
  run_migration >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -cE '^STAMP ' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}
