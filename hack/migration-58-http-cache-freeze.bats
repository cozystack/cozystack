#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 58 --> 59 (freeze HTTPCache).
#
# The migration branches on whether an HTTPCache instance exists: it freezes the
# application on a pinned source, or removes the platform side. See
# packages/core/platform/images/migrations/migrations/58 for the mechanism.
#
# FAIL CLOSED is pinned for each path: a failed fleet scan, a failed read of the
# source OCIRepository, a source with no digest to freeze on, a failed patch or a
# failed delete must stop the migration before it stamps the version, because
# migrations never re-run.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-58-http-cache-freeze.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-58-http-cache"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration58-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
CHART=cozystack-http-cache-application-default-http-cache
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-58-$$"

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}
# What the fake's live source carries: a spec.ref.digest pin, and the manifest
# digest its status.artifact.revision resolves a tag ref to.
SPEC_DIGEST=sha256:1111111111111111111111111111111111111111111111111111111111111111
RESOLVED_DIGEST=sha256:2222222222222222222222222222222222222222222222222222222222222222
GRANT=cozy:tenant:admin:http-cache-frozen

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
    -e FAKE_HRS="${FAKE_HRS-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_OCIREPO_REF="${FAKE_OCIREPO_REF-}" \
    -e FAKE_OCIREPO_READ_FAIL="${FAKE_OCIREPO_READ_FAIL-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    -e FAKE_DELETE_FAIL="${FAKE_DELETE_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 58 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_HRS=""
  unset FAKE_LIST_FAIL FAKE_OCIREPO_REF FAKE_OCIREPO_READ_FAIL FAKE_PATCH_FAIL FAKE_DELETE_FAIL || true
  return 0
}

cmdlog_line() {
  grep -nE -- "$1" "$FAKE_CMDLOG" | head -1 | cut -d: -f1
}

@test "with instances it freezes the application on the live digest and deletes nothing" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache
tenant-b http-cache-old some-old-chart HTTPCache
tenant-a redis-cache cozystack-redis-application-default-redis Redis"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # The frozen source is a copy: the url, the pull secret and the interval come
  # from the live OCIRepository, the digest is the one it is pinned to, nothing
  # owns it, and it carries the deletion guardrail the original has.
  grep -qE "^APPLY-OCIREPO cozystack-packages-http-cache $SPEC_DIGEST oci://registry.example.org/cozystack/cozystack-packages registry-creds 5m verify=cosign owners=0 nodelete=1$" "$FAKE_CMDLOG"

  # The application is repointed at it, kept from the prune and disowned. Both
  # halves of the disown matter: the annotation null keeps Helm's prune off it,
  # the managed-by null stops a later release adopting it.
  grep -qE "^PATCH-PACKAGESOURCE keep=1 source=1 disown=1 managed=1$" "$FAKE_CMDLOG"
  grep -qE "^PATCH-PACKAGE$" "$FAKE_CMDLOG"

  # The chart no longer names httpcaches in cozy:tenant:admin:base, so without
  # this grant the owner of a frozen instance could see it and not remove it.
  grep -qE "^APPLY-CLUSTERROLE $GRANT aggregate=1 nodelete=1 resources=httpcaches verbs=create,update,patch,delete$" "$FAKE_CMDLOG"

  # Freezing must not remove anything: the instances keep running off it.
  if grep -qE "^DELETE-" "$FAKE_CMDLOG"; then echo "unexpected delete while freezing" >&2; return 1; fi
  grep -qF "STAMP 59" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "with a tag ref it freezes on the digest the tag resolved to" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache"
  export FAKE_OCIREPO_REF=tag
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # A tag can move under the frozen copy, so the pin is the manifest digest the
  # tag resolved to, not the tag itself and not the tarball checksum.
  grep -qE "^APPLY-OCIREPO cozystack-packages-http-cache $RESOLVED_DIGEST oci://registry.example.org/cozystack/cozystack-packages registry-creds 5m verify=cosign owners=0 nodelete=1$" "$FAKE_CMDLOG"
  grep -qE "^PATCH-PACKAGESOURCE keep=1 source=1 disown=1 managed=1$" "$FAKE_CMDLOG"
  grep -qF "STAMP 59" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a source with no digest to freeze on aborts before repointing and before stamping" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache"
  export FAKE_OCIREPO_REF=unresolved
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^(APPLY-|PATCH-)" "$FAKE_CMDLOG"; then echo "unexpected action with nothing to freeze on" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP with nothing to freeze on" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "with no instances it removes the platform side, Package first, and freezes nothing" {
  prep
  export FAKE_HRS="tenant-a redis-cache cozystack-redis-application-default-redis Redis"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # wait=1, not merely first in the log: the client has to block until the
  # Package is gone, or an in-flight reconcile re-creates what follows it here.
  _pkg=$(cmdlog_line "^DELETE-PACKAGE cozystack.http-cache-application wait=1$")
  _rd=$(cmdlog_line "^DELETE-HR cozy-system http-cache-rd$")
  _ad=$(cmdlog_line "^DELETE-APPDEF http-cache$")
  _sec=$(cmdlog_line "^DELETE-SECRETS cozy-system owner=helm,name=http-cache-rd$")
  [ -n "$_pkg" ] && [ -n "$_rd" ] && [ -n "$_ad" ] && [ -n "$_sec" ] || { echo "missing cleanup step: pkg=$_pkg rd=$_rd appdef=$_ad secrets=$_sec" >&2; return 1; }
  [ "$_pkg" -lt "$_rd" ] || { echo "http-cache-rd deleted before its Package" >&2; return 1; }

  # Nothing is pinned or granted on a cluster that never ran the application.
  if grep -qE "^APPLY-OCIREPO" "$FAKE_CMDLOG"; then echo "unexpected frozen source with no instances" >&2; return 1; fi
  if grep -qE "^APPLY-CLUSTERROLE" "$FAKE_CMDLOG"; then echo "unexpected tenant grant with no instances" >&2; return 1; fi
  if grep -qE "^PATCH-" "$FAKE_CMDLOG"; then echo "unexpected patch with no instances" >&2; return 1; fi
  grep -qF "STAMP 59" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing fleet scan aborts before touching anything and before stamping" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache"
  export FAKE_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "^(APPLY-|PATCH-|DELETE-)" "$FAKE_CMDLOG"; then echo "unexpected action after failed fleet scan" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed fleet scan" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing read of the source OCIRepository aborts before repointing and before stamping" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache"
  export FAKE_OCIREPO_READ_FAIL="Error from server (Forbidden): ocirepositories.source.toolkit.fluxcd.io \"cozystack-packages\" is forbidden"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # A swallowed read would publish a source pointing nowhere and repoint the
  # application at it, which is worse than stopping.
  [ "$rc" -ne 0 ]
  if grep -qE "^(APPLY-|PATCH-)" "$FAKE_CMDLOG"; then echo "unexpected action after failed OCIRepository read" >&2; return 1; fi
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed OCIRepository read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing patch aborts before stamping" {
  prep
  export FAKE_HRS="tenant-a http-cache-web $CHART HTTPCache"
  export FAKE_PATCH_FAIL="Error from server (Conflict): the object has been modified"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed patch" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing cleanup delete aborts before stamping" {
  prep
  export FAKE_HRS=""
  export FAKE_DELETE_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 58 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed delete" >&2; return 1; fi
  rm -rf "$WORK"
}
