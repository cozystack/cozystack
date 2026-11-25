#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 63 --> 64 (report the tenant Kubernetes
# clusters that stay on a shared tenant etcd). See
# packages/core/platform/images/migrations/migrations/63 for the mechanism.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
# -----------------------------------------------------------------------------

load test_helper

FIXTURES="$PWD/hack/testdata/migration-63-shared-etcd"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed --quiet 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration63-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-63-$$"

cozy_cleanup() {
  rm --recursive --force "$WORKROOT"
  return 0
}

run_migration() {
  _run_migration_rc=0
  docker run --rm --network none \
    --user "$(id --user):$(id --group)" \
    --volume "$MIG_DIR:/migrations:ro" \
    --volume "$FIXTURES:/fakebin:ro" \
    --volume "$WORK:/work" \
    --env PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    --env FAKE_CMDLOG=/work/cmdlog \
    --env FAKE_STATE=/work/state \
    --env NAMESPACE="${NAMESPACE-}" \
    --env FAKE_NO_KAMAJI="${FAKE_NO_KAMAJI-}" \
    --env FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 63 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build --quiet --tag "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache coreutils jq
DOCKERFILE
  mkdir --parents "$WORKROOT"
  WORK=$(mktemp --directory "$WORKROOT/XXXXXX")
  mkdir --parents "$WORK/state"
  cp "$FIXTURES/kcps.json" "$FIXTURES/datastores.json" "$WORK/state/"
  FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  unset FAKE_NO_KAMAJI FAKE_LIST_FAIL || true
  return 0
}

@test "lists every cluster whose DataStore its own release did not render, and changes nothing" {
  prep
  rc=0
  run_migration 63 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  grep --quiet --line-regexp --fixed-strings "3 tenant Kubernetes cluster(s) stay on a shared tenant etcd." "$WORK/out"
  grep --quiet --line-regexp --fixed-strings "  shared-etcd: tenant-a/kubernetes-legacy DataStore=tenant-root endpoints=etcd.tenant-root.svc:2379" "$WORK/out"
  grep --quiet --line-regexp --fixed-strings "  shared-etcd: tenant-b/kubernetes-nearer DataStore=tenant-b endpoints=etcd.tenant-b.svc:2379" "$WORK/out"
  # A DataStore another cluster's release renders is not this cluster's own.
  grep --quiet --line-regexp --fixed-strings "  shared-etcd: tenant-b/kubernetes-borrowed DataStore=tenant-a.kubernetes-own endpoints=kubernetes-own-etcd.tenant-a.svc:2379" "$WORK/out"
  if grep --quiet --fixed-strings "tenant-a/kubernetes-own " "$WORK/out"; then echo "FAIL: a cluster on its own etcd was reported" >&2; false; fi

  # Report only: the sole write is the version stamp.
  if grep --extended-regexp "KUBECTL (patch|delete|annotate|label|create|replace|edit)" "$FAKE_CMDLOG"; then echo "FAIL: the migration wrote something" >&2; false; fi
  grep --quiet --line-regexp --fixed-strings "STAMP 64" "$FAKE_CMDLOG"
  rm --recursive --force "$WORK"
}

@test "says so when no cluster remains on a shared etcd" {
  prep
  cp "$FIXTURES/none-kcps.json" "$WORK/state/kcps.json"
  rc=0
  run_migration 63 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep --quiet --fixed-strings "none remains on a shared DataStore" "$WORK/out"
  grep --quiet --line-regexp --fixed-strings "STAMP 64" "$FAKE_CMDLOG"
  rm --recursive --force "$WORK"
}

@test "a cluster without Kamaji only stamps the version" {
  prep
  export FAKE_NO_KAMAJI=1
  rc=0
  run_migration 63 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep --quiet --extended-regexp "get (kamajicontrolplanes|datastores)" "$FAKE_CMDLOG"; then echo "FAIL: listed control planes without the CRD" >&2; false; fi
  grep --quiet --line-regexp --fixed-strings "STAMP 64" "$FAKE_CMDLOG"
  rm --recursive --force "$WORK"
}

@test "a failing control-plane list aborts before stamping" {
  prep
  export FAKE_LIST_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 63 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep --quiet --fixed-strings "STAMP" "$FAKE_CMDLOG"; then echo "FAIL: stamped after a failed list" >&2; false; fi
  rm --recursive --force "$WORK"
}
