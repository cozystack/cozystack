#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 55 --> 56 (remove the tenant-cluster FluxCD
# addon). PR #3379 drops packages/system/fluxcd{,-operator}, the last vendored
# copyleft charts, which only backed the optional `addons.fluxcd` toggle of the
# Kubernetes app. For any tenant that had it enabled, the addon's pair of
# HelmReleases must be ORPHANED, not pruned: suspend reconciliation, drop the
# Flux finalizer so the delete does not trigger a Helm uninstall inside the
# tenant cluster, verify the finalizer is actually gone, and only then delete the
# now-inert object from the management cluster. The tenant's own Flux keeps
# running untouched.
#
# Two properties are pinned here:
#
#  1. ORDERING + SELECTION. Only the two fluxcd-addon HelmReleases (matched by
#     .spec.chartRef.name) may be touched, and each must go through
#     suspend -> finalizer-drop -> delete IN THAT ORDER. Deleting before the
#     finalizer is gone would set a deletionTimestamp that makes helm-controller
#     run the very uninstall this migration exists to avoid; touching an
#     unrelated HelmRelease would orphan a release the migration has no business
#     touching.
#
#  2. FAIL CLOSED. Removing the finalizer is the ONLY thing that keeps the delete
#     inert. Migrations never re-run, so a swallowed error here would stamp the
#     version on a half-orphaned fleet and never look back. If the finalizer
#     survives the patch (webhook, RBAC, conflict, transient apiserver error) the
#     migration must abort BEFORE deleting and BEFORE stamping — recoverable on
#     the Job's next attempt, rather than a Helm uninstall that is not.
#
# These drive the real migration script end-to-end against a fake kubectl
# (hack/testdata/migration-55-fluxcd/), mocking only the cluster boundary.
#
# SHELL. Production runs migration 55 under /bin/sh = busybox ash: the migrations
# image is FROM alpine and run-migrations.sh execs `/migrations/<n>` BY PATH, so
# the kernel honours the `#!/bin/sh` shebang. `set -euo pipefail` and its
# errexit-in-function semantics differ from bash, and pipefail is load-bearing:
# the fleet scan pipes `kubectl ... -o json` into jq, so without it a failed list
# would not abort the pipeline and the loop would run against empty input. So,
# exactly as hack/migration-seaweedfs-db-adopt.bats does, run_migration() runs the
# script by path inside the image's own pinned base rather than through a host
# shell — same base image, same interpreter, same invocation form.
#
# jq. Migration 55, unlike the seaweedfs migrations, pipes through jq. The raw
# alpine base ships without it while the built migrations image installs it via
# `apk add`, so prep() bakes jq onto the pinned base once (the only step that
# touches the network) and run_migration() uses that image, still --network none.
# Same base, same busybox ash, plus the one tool the script actually requires;
# the selection logic under test is jq's own, run for real.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its own
# line, rewriting the latter into `return 0` + `}`; there is no bats
# `run`/`$status`/`setup`. A helper whose exit status matters must therefore
# capture it and `return` it by hand before its closing brace, or the injected
# `return 0` would mask it (see run_migration below). Assertions are direct shell
# tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-55-fluxcd-orphan.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-55-fluxcd"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"

# The production base image, read out of the migrations Dockerfile rather than
# repeated here, so the interpreter under test cannot drift from the one the
# migrations actually ship on when that pin is bumped.
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)

# jq-enabled build of that base. The tag is derived from the pinned ref so a
# digest bump rebuilds it instead of reusing a stale layer.
TESTIMG="cozystack-migration55-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"

# run_migration <n> -- run migrations/<n> the way run-migrations.sh does.
#
# By path, not `sh <file>`: that is what makes the shebang, and therefore the
# interpreter, part of what is under test. The fake kubectl goes on PATH inside
# the container and $WORK is bind-mounted, so $FAKE_CMDLOG is the same file the
# assertions read back on the host. --network none because nothing here may reach
# a real cluster; --user keeps $WORK removable by the test afterwards.
#
# The explicit `return` is load-bearing: cozytest.sh's awk generator rewrites
# every bare `}` in column 0 into `return 0` + `}`, so a helper that falls off
# its own end returns 0 no matter what it ran, and every fail-closed assertion
# below would pass vacuously. Capture the status and return it by hand.
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
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    -e FAKE_DELETE_FAIL="${FAKE_DELETE_FAIL-}" \
    -e FAKE_FINALIZER_STICKS="${FAKE_FINALIZER_STICKS-}" \
    -e FAKE_VERIFY_READ_FAIL="${FAKE_VERIFY_READ_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

# prep resets env to a clean scenario. Tests set FAKE_* afterwards.
prep() {
  # Fail here rather than at the first docker run, so the reason is legible.
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 55 inside a jq-enabled" >&2
    echo "build of $ALPINE (the migrations image's base), so that it exercises" >&2
    echo "busybox ash — the interpreter run-migrations.sh actually gives it." >&2
    return 1
  }
  # Bake jq onto the pinned base. Cached after the first run, so this is a no-op
  # on every subsequent test; only the first build touches the network.
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_HRS=""
  unset FAKE_LIST_FAIL FAKE_PATCH_FAIL FAKE_DELETE_FAIL FAKE_FINALIZER_STICKS FAKE_VERIFY_READ_FAIL || true
  return 0
}

# cmdlog_line <ere> -- 1-based line number of the first cmdlog line matching the
# anchored extended regex, or empty if none. Used only for its stdout, so the
# awk-injected `return 0` is harmless.
cmdlog_line() {
  grep -nE -- "$1" "$FAKE_CMDLOG" | head -1 | cut -d: -f1
}

# assert_order <ns> <name> -- the HelmRelease was suspended, then
# finalizer-patched, then deleted, in that cmdlog order. Anchored with `$` so
# `...-fluxcd` does not also match `...-fluxcd-operator`.
assert_order() {
  _s=$(cmdlog_line "^SUSPEND $1 $2$")
  _f=$(cmdlog_line "^FINALIZER-PATCH $1 $2$")
  _d=$(cmdlog_line "^DELETE $1 $2$")
  [ -n "$_s" ] && [ -n "$_f" ] && [ -n "$_d" ] || { echo "missing step for $1/$2: suspend=$_s finalizer=$_f delete=$_d" >&2; return 1; }
  [ "$_s" -lt "$_f" ] && [ "$_f" -lt "$_d" ] || { echo "out-of-order for $1/$2: suspend=$_s finalizer=$_f delete=$_d" >&2; return 1; }
  return 0
}

# --- 1. ordering + selection ------------------------------------------------

@test "orphans both addon HelmReleases in each namespace, in order, and ignores unrelated releases" {
  prep
  # Two tenants each carry the fluxcd + fluxcd-operator addon HelmReleases, plus
  # one unrelated release (the base kubernetes app) that must be left alone.
  export FAKE_HRS="tenant-a kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd
tenant-a kubevirt-kubernetes-fluxcd-operator cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd-operator
tenant-b kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd
tenant-b kubevirt-kubernetes-fluxcd-operator cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd-operator
tenant-a some-other-app cozystack-kubernetes-application-kubevirt-kubernetes"
  rc=0
  run_migration 55 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # Every matching HelmRelease went suspend -> finalizer-drop -> delete, in order.
  assert_order tenant-a kubevirt-kubernetes-fluxcd
  assert_order tenant-a kubevirt-kubernetes-fluxcd-operator
  assert_order tenant-b kubevirt-kubernetes-fluxcd
  assert_order tenant-b kubevirt-kubernetes-fluxcd-operator

  # Exactly the four matching releases were acted on — no more.
  [ "$(grep -cE '^SUSPEND ' "$FAKE_CMDLOG")" -eq 4 ]
  [ "$(grep -cE '^FINALIZER-PATCH ' "$FAKE_CMDLOG")" -eq 4 ]
  [ "$(grep -cE '^DELETE ' "$FAKE_CMDLOG")" -eq 4 ]

  # The unrelated release never appears in any acted-on command.
  if grep -q 'some-other-app' "$FAKE_CMDLOG"; then echo "unexpected some-other-app in cmdlog" >&2; return 1; fi

  # Version stamped to 56 — asserting the number, not a bare "STAMP": a wrong
  # version would loop run-migrations.sh forever.
  grep -qF -- "STAMP 56" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# --- 2. fail closed ---------------------------------------------------------

# The finalizer survives the patch. The migration must refuse to delete and must
# not stamp: the Job retries rather than uninstalling the tenant's live Flux.
@test "a finalizer surviving the patch aborts before delete and before stamping" {
  prep
  export FAKE_HRS="tenant-a kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd
tenant-b kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd"
  export FAKE_FINALIZER_STICKS=1
  rc=0
  run_migration 55 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"

  # Must propagate: a swallowed error would stamp past a half-orphaned fleet.
  [ "$rc" -ne 0 ]
  # It got as far as suspend + finalizer-drop on the first release before the
  # verify caught the still-present finalizer...
  grep -qE -- "^SUSPEND tenant-a kubevirt-kubernetes-fluxcd$" "$FAKE_CMDLOG"
  grep -qE -- "^FINALIZER-PATCH tenant-a kubevirt-kubernetes-fluxcd$" "$FAKE_CMDLOG"
  grep -qF -- "refusing to delete" "$WORK/out"
  # ...and it deleted nothing and stamped nothing.
  if grep -qE -- "^DELETE " "$FAKE_CMDLOG"; then echo "unexpected DELETE after stuck finalizer" >&2; return 1; fi
  if grep -qF -- "STAMP 56" "$FAKE_CMDLOG"; then echo "unexpected STAMP 56 after stuck finalizer" >&2; return 1; fi
  rm -rf "$WORK"
}

# The verify READ that gates the delete errors out (RBAC, conflict, a transient
# apiserver hiccup). The verify read uses `--ignore-not-found` rather than
# `2>/dev/null || true`, so a failed read aborts under set -e instead of being
# swallowed to an empty string that reads as "finalizer gone" — a failed read is
# NOT proof the object is safe to delete. The suspend and finalizer patches
# before it succeed; the migration must still refuse to delete and must not
# stamp. Without FAKE_VERIFY_READ_FAIL this path is the one the fail-open bug hid
# in.
@test "a failing finalizer verify read aborts before delete and before stamping" {
  prep
  export FAKE_HRS="tenant-a kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd
tenant-b kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd"
  export FAKE_VERIFY_READ_FAIL="Error from server (Forbidden): helmreleases.helm.toolkit.fluxcd.io \"kubevirt-kubernetes-fluxcd\" is forbidden"
  rc=0
  run_migration 55 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"

  # Must propagate: a swallowed read error would pass the gate on an object whose
  # finalizer state was never actually read, then delete and stamp past it.
  [ "$rc" -ne 0 ]
  # It got as far as suspend + finalizer-drop on the first release before the
  # verify read failed...
  grep -qE -- "^SUSPEND tenant-a kubevirt-kubernetes-fluxcd$" "$FAKE_CMDLOG"
  grep -qE -- "^FINALIZER-PATCH tenant-a kubevirt-kubernetes-fluxcd$" "$FAKE_CMDLOG"
  # ...and it deleted nothing and stamped nothing.
  if grep -qE -- "^DELETE " "$FAKE_CMDLOG"; then echo "unexpected DELETE after failed verify read" >&2; return 1; fi
  if grep -qF -- "STAMP 56" "$FAKE_CMDLOG"; then echo "unexpected STAMP 56 after failed verify read" >&2; return 1; fi
  rm -rf "$WORK"
}

# The fleet scan itself fails. Under `set -euo pipefail` the failing
# `kubectl ... | jq` aborts the `hrs=$(...)` assignment instead of yielding an
# empty list the loop would silently skip — the pipefail the header calls
# load-bearing. Nothing is touched and, crucially, the version is not stamped
# past a fleet that was never actually inspected.
@test "a failing fleet scan aborts before stamping" {
  prep
  export FAKE_HRS="tenant-a kubevirt-kubernetes-fluxcd cozystack-kubernetes-application-kubevirt-kubernetes-fluxcd"
  export FAKE_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 55 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: the Job retries rather than advancing the version.
  [ "$rc" -ne 0 ]
  if grep -q 'STAMP' "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed fleet scan" >&2; return 1; fi
  # The scan failed before the loop, so not even the first release was touched.
  if grep -qE -- "^SUSPEND " "$FAKE_CMDLOG"; then echo "unexpected SUSPEND after failed fleet scan" >&2; return 1; fi
  rm -rf "$WORK"
}
