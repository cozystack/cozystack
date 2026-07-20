#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the SeaweedFS db-split hand-over shared by platform migrations
# 43 (original) and 53 (repair) — lib/seaweedfs-db-adopt.sh.
#
# The 1.5.0 split (PR #2601) moved Cluster/seaweedfs-db out of the <name>-system
# release into a new <name>-db release. The hand-over must, before <name>-system
# next renders, re-own the Cluster to <name>-db AND stamp
# helm.sh/resource-policy: keep — otherwise the <name>-system upgrade prunes the
# Cluster as a removed resource, CNPG takes its PVC with it, and the tenant's
# filer metadata (all of its S3) is gone.
#
# Three properties are pinned here, each of which shipped broken:
#
#  1. INSTANCE NAME. Migration 43 compared the owner against the literal
#     "seaweedfs-system". `SeaweedFS` is user-creatable, so an instance named
#     `foo` is owned by `foo-system` and was silently skipped. Observed live:
#     four default-named tenants carry release-name=seaweedfs-db + keep and their
#     Clusters survived; the one tenant running `foo` has no Cluster at all.
#
#  2. OWNERSHIP IS NOT SAFETY. A Cluster already owned by <name>-db can still
#     need keep: where the hand-over was skipped, <name>-system prunes it and
#     <name>-db RECREATES it under its own ownership with no keep, while
#     <name>-system's prune baseline still lists it. Live proof that the shape is
#     real: tenant-l and tenant-root are <name>-db-owned and their <name>-system
#     deployed revision still contains the Cluster — only keep saves them.
#
#  3. FAIL CLOSED. Migrations never re-run, so a swallowed error permanently
#     leaves at-risk tenants exposed. `for ns in $(kubectl ...)` does not trip
#     errexit: on failure the loop runs zero times and the script stamps the
#     version anyway. Only "the resource type is not served" (no CNPG at all) and
#     "gone between scan and read" may be treated as empty.
#
# These drive the real migration scripts end-to-end against a fake kubectl
# (hack/testdata/migration-seaweedfs-db/), mocking only the cluster boundary.
#
# SHELL. Production runs these under /bin/sh = busybox ash: the migrations image
# is FROM alpine, and run-migrations.sh execs `/migrations/<n>` BY PATH, so the
# kernel honours the `#!/bin/sh` shebang. `set -euo pipefail` and
# errexit-in-function semantics differ from bash, and pipefail is load-bearing —
# lib/cozystack-version.sh pipes the rendered manifest into `kubectl apply`, so
# without it a failed render stamps the version from empty input. Asserting
# fail-closed in a shell that never runs it would be asserting nothing.
#
# So these tests do not invoke the migrations through the runner's shell at all:
# run_migration() executes them by path inside the image's own pinned base, read
# from the migrations Dockerfile. Same base image, same interpreter, same
# invocation form — production, rather than an approximation of it. This needs a
# working docker; there is deliberately no host-shell fallback, because every
# fallback available is a shell production never uses.
#
# What that replaced was `sh "$MIG_DIR/<n>"`, which is neither production nor
# bash: on the GitHub ubuntu runner /bin/sh is dash, which has no `set -o
# pipefail` and aborts on line 1 with "set: Illegal option -o pipefail" before
# the script does any work, while on a developer box /bin/sh is often bash, which
# runs green and proves nothing about ash.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run`/`$status`/`setup`. Assertions are direct
# shell tests that exit non-zero on failure.
#
# Run with: hack/cozytest.sh hack/migration-seaweedfs-db-adopt.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-seaweedfs-db"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"

# The production base image, read out of the migrations Dockerfile rather than
# repeated here, so the interpreter under test cannot drift from the one the
# migrations actually ship on when that pin is bumped.
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)

# run_migration <n> -- run migrations/<n> the way run-migrations.sh does.
#
# By path, not `sh <file>`: that is what makes the shebang, and therefore the
# interpreter, part of what is under test. The fake kubectl goes on PATH inside
# the container and $WORK is bind-mounted, so $FAKE_CMDLOG is the same file the
# assertions read back on the host. --network none because nothing here may
# reach a real cluster; --user keeps $WORK removable by the test afterwards.
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
    -e FAKE_CLUSTERS="${FAKE_CLUSTERS-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_GET_FAIL="${FAKE_GET_FAIL-}" \
    -e FAKE_ANNOTATE_FAIL="${FAKE_ANNOTATE_FAIL-}" \
    "$ALPINE" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

# prep resets env to a clean scenario. Tests set FAKE_* afterwards.
prep() {
  # Fail here rather than at the first docker run, so the reason is legible.
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run the migrations inside $ALPINE," >&2
    echo "the base image of the migrations image, so that they exercise busybox" >&2
    echo "ash — the interpreter run-migrations.sh actually gives them." >&2
    return 1
  }
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  export FAKE_CLUSTERS=""
  unset FAKE_LIST_FAIL FAKE_GET_FAIL FAKE_ANNOTATE_FAIL || true
}

# --- 1. instance name -------------------------------------------------------

@test "hands over a default-named instance (seaweedfs-system -> seaweedfs-db)" {
  prep
  export FAKE_CLUSTERS="tenant-root seaweedfs-system -"
  rc=0
  run_migration 43 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-root release-name=seaweedfs-db resource-policy=keep" "$FAKE_CMDLOG"
  # Migration 43 stamps 44 — asserting the number, not a bare "STAMP": a wrong
  # version would loop run-migrations.sh forever.
  grep -qF -- "STAMP 44" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# THE original regression. Unfixed (owner compared against the literal
# "seaweedfs-system") this namespace is skipped entirely: no ANNOTATE line, and
# the Cluster is left with no keep for the foo-system upgrade to prune.
@test "hands over a NON-default instance name (foo-system -> foo-db)" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  rc=0
  run_migration 43 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=foo-db resource-policy=keep" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "migration 53 repairs a non-default instance the hardcoded 43 skipped, and stamps 54" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=foo-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "STAMP 54" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "handles a mixed fleet: every -system owner is handed over, in its own namespace" {
  prep
  # The shape of the upgrade stand: default-named tenants plus one `foo`.
  export FAKE_CLUSTERS="tenant-root seaweedfs-system -
tenant-dsplit seaweedfs-system -
tenant-l seaweedfs-system -
tenant-named foo-system -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-root release-name=seaweedfs-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "ANNOTATE tenant-dsplit release-name=seaweedfs-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "ANNOTATE tenant-l release-name=seaweedfs-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "ANNOTATE tenant-named release-name=foo-db resource-policy=keep" "$FAKE_CMDLOG"
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 4 ]
  rm -rf "$WORK"
}

# --- 2. ownership is not safety --------------------------------------------

# A <name>-db-owned Cluster WITHOUT keep is exposed, not done: <name>-system's
# prune baseline may still list the Cluster (live on the stand for tenant-l and
# tenant-root), in which case its next reconcile deletes it. Skipping on
# ownership alone — the shape the previous revision of this helper shipped —
# leaves the database to be pruned.
@test "protects a <name>-db-owned Cluster that is still missing keep" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-db -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=<unset> resource-policy=keep" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "protects a default-named <name>-db-owned Cluster that is still missing keep" {
  prep
  export FAKE_CLUSTERS="tenant-fresh seaweedfs-db -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-fresh release-name=<unset> resource-policy=keep" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "idempotent: a Cluster already owned by <name>-db AND carrying keep is left alone" {
  prep
  export FAKE_CLUSTERS="tenant-root seaweedfs-db keep
tenant-named foo-db keep"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "STAMP 54" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "leaves a Cluster with no Helm owner annotation alone, but says so" {
  prep
  # Not Helm-managed. Guessing an owner would be worse than doing nothing, but
  # an unowned SeaweedFS database must not pass silently.
  export FAKE_CLUSTERS="tenant-manual - -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "carries no meta.helm.sh/release-name" "$WORK/out"
  rm -rf "$WORK"
}

@test "leaves a Cluster owned by an unrelated release alone" {
  prep
  export FAKE_CLUSTERS="tenant-x some-other-release -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "owned by unrelated release" "$WORK/out"
  rm -rf "$WORK"
}

@test "refuses a release literally named -system rather than annotating owner -db" {
  prep
  # "${current%-system}" would be empty, yielding release-name=-db, which no
  # release will ever claim: the Cluster would be orphaned by the very step
  # meant to protect it.
  export FAKE_CLUSTERS="tenant-weird -system -"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "no instance name" "$WORK/out"
  rm -rf "$WORK"
}

# --- 3. fail closed ---------------------------------------------------------

@test "a failing fleet scan aborts the migration instead of stamping past it" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: the Job retries rather than advancing the version.
  [ "$rc" -ne 0 ]
  grep -qF -- "refusing to stamp past an unverified fleet" "$WORK/out"
  ! grep -q 'STAMP' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an unreadable owner annotation aborts rather than being read as not-Helm-managed" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_GET_FAIL="Error from server (Forbidden): clusters.postgresql.cnpg.io \"seaweedfs-db\" is forbidden"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "cannot read the Helm owner" "$WORK/out"
  ! grep -q 'STAMP' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed hand-over aborts rather than stamping a half-migrated fleet" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_ANNOTATE_FAIL="Error from server (Conflict): the object has been modified"
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  ! grep -q 'STAMP' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# The fail-open that IS load-bearing: a cluster with no CNPG at all must not be
# blocked from upgrading. "The server doesn't have a resource type" is the only
# list failure allowed to mean "nothing to do".
@test "a cluster with no CNPG resource type stamps cleanly without annotating" {
  prep
  export FAKE_LIST_FAIL="error: the server doesn't have a resource type \"cluster\" in group \"postgresql.cnpg.io\""
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "resource type is not served" "$WORK/out"
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "STAMP 54" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an empty fleet stamps without annotating" {
  prep
  export FAKE_CLUSTERS=""
  rc=0
  run_migration 53 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  ! grep -q 'ANNOTATE' "$FAKE_CMDLOG"
  grep -qF -- "STAMP 54" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
