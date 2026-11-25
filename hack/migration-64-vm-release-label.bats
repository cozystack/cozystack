#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 64 --> 65 (label existing worker VMs with
# their cluster's HelmRelease). See
# packages/core/platform/images/migrations/migrations/64 for the mechanism.
#
# The fake keeps the VMs in a state file and applies each label call to it, so
# a second run reads what the first one wrote.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-64-vm-release-label.bats
# -----------------------------------------------------------------------------

load test_helper

FIXTURES="$PWD/hack/testdata/migration-64-vm-release-label"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration64-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-64-$$"

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

run_migration() {
  _run_migration_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$MIG_DIR:/migrations:ro" \
    -v "$FIXTURES:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_CMDLOG=/work/cmdlog \
    -e FAKE_STATE=/work/state \
    -e NAMESPACE="${NAMESPACE-}" \
    -e FAKE_NO_KUBEVIRT="${FAKE_NO_KUBEVIRT-}" \
    -e FAKE_CRD_FAIL="${FAKE_CRD_FAIL-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_LABEL_FAIL="${FAKE_LABEL_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

# build_test_image bakes the test's packages onto the pinned base. Its status
# is docker build's, so a failed build fails whoever called it.
build_test_image() {
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
}

# Build the image once per file, before any test runs. Tests in a file run in
# parallel under `bats -j`, and concurrent uncached builds of one tag replace
# each other's image, so a sibling's `docker run` could find the tag missing
# and fall back to a pull. Without docker, prep() reports why.
setup_file() {
  docker info >/dev/null 2>&1 || return 0
  build_test_image
  return 0
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 64 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  # setup_file built it already; this only builds when the file runs without
  # bats' setup_file, one test at a time.
  docker image inspect "$TESTIMG" >/dev/null 2>&1 || build_test_image
  chmod +x "$FIXTURES/kubectl"
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  mkdir -p "$WORK/state"
  cp "$FIXTURES/vms.json" "$WORK/state/"
  FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  unset FAKE_NO_KUBEVIRT FAKE_CRD_FAIL FAKE_LIST_FAIL FAKE_LABEL_FAIL || true
  return 0
}

@test "labels each CAPK worker VM with the release its cluster-name label names" {
  prep
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # The value is the CAPI Cluster's name, which is the parent release under the
  # default naming and under an override such as the ComputePlane one.
  grep -qxF "LABEL-VM tenant-a/kubernetes-a-md0-x1 helm.toolkit.fluxcd.io/name=kubernetes-a" "$FAKE_CMDLOG"
  grep -qxF "LABEL-VM tenant-b/computeplane-cluster-md0-y1 helm.toolkit.fluxcd.io/name=computeplane-cluster" "$FAKE_CMDLOG"

  # A VM that already names a release keeps it, and a VM without a cluster to
  # name, one CAPK did not create, or one whose KubevirtMachine lives in another
  # namespace is left alone.
  [ "$(grep -c "^LABEL-VM " "$FAKE_CMDLOG")" -eq 2 ]
  grep -qxF "STAMP 65" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a second run finds nothing to change" {
  prep
  run_migration 64 >"$WORK/out" 2>&1
  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qF "LABEL-VM" "$FAKE_CMDLOG"; then echo "second run labelled a VM" >&2; return 1; fi
  grep -qxF "STAMP 65" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a cluster without KubeVirt only stamps the version" {
  prep
  export FAKE_NO_KUBEVIRT=1
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "get virtualmachines|LABEL-VM" "$FAKE_CMDLOG"; then echo "unexpected VM access" >&2; return 1; fi
  grep -qxF "STAMP 65" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing CRD read aborts instead of treating KubeVirt as absent" {
  prep
  export FAKE_CRD_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "$FAKE_CRD_FAIL" "$WORK/out"
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed CRD read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing VM list aborts before stamping" {
  prep
  export FAKE_LIST_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "$FAKE_LIST_FAIL" "$WORK/out"
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed list" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing label call aborts before stamping" {
  prep
  export FAKE_LABEL_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 64 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "$FAKE_LABEL_FAIL" "$WORK/out"
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed label" >&2; return 1; fi
  rm -rf "$WORK"
}
