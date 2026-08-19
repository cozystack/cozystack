#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the platform migrations in slots 43 and 45 — driven end-to-end
# against a fake kubectl, so what is under test is the migration scripts rather
# than their helpers in isolation.
#
# Slot 45 does TWO independent jobs on release-1.5, and both are covered here
# because they run in one script and share one version stamp:
#
#   1. lib/seaweedfs-db-adopt.sh — the SeaweedFS db-split hand-over, shared with
#      migration 43. Sections 1-3 below.
#   2. lib/kubeadm-keep-pin.sh — the helm.sh/resource-policy=keep pin on the CAPI
#      kubeadm bootstrap objects, which is 1.6's own migration 45 folded into this
#      slot because a v1.5.4 cluster is stamped 46 and its later 1.6 upgrade runs
#      `seq 46 53`, skipping 1.6's slot 45 entirely. Section 4 below.
#
# --- 1-3: the SeaweedFS hand-over ---
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
# errexit-in-function semantics differ from bash, and both are load-bearing here:
# the fail-closed returns from adopt_seaweedfs_db_clusters have to abort the
# script before it reaches its stamp. Asserting fail-closed in a shell that never
# runs it would be asserting nothing.
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
    -e FAKE_KCTS="${FAKE_KCTS-}" \
    -e FAKE_KCS="${FAKE_KCS-}" \
    -e FAKE_KUBEADM_LIST_FAIL="${FAKE_KUBEADM_LIST_FAIL-}" \
    -e FAKE_KUBEADM_ANNOTATE_FAIL="${FAKE_KUBEADM_ANNOTATE_FAIL-}" \
    -e FAKE_KUBEADM_ANNOTATE_FAIL_NS="${FAKE_KUBEADM_ANNOTATE_FAIL_NS-}" \
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
  export FAKE_KCTS=""
  export FAKE_KCS=""
  unset FAKE_LIST_FAIL FAKE_GET_FAIL FAKE_ANNOTATE_FAIL || true
  unset FAKE_KUBEADM_LIST_FAIL FAKE_KUBEADM_ANNOTATE_FAIL \
        FAKE_KUBEADM_ANNOTATE_FAIL_NS || true
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

@test "migration 45 repairs a non-default instance the hardcoded 43 skipped, and stamps 46" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=foo-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
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
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
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
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=<unset> resource-policy=keep" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "protects a default-named <name>-db-owned Cluster that is still missing keep" {
  prep
  export FAKE_CLUSTERS="tenant-fresh seaweedfs-db -"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
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
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "leaves a Cluster with no Helm owner annotation alone, but says so" {
  prep
  # Not Helm-managed. Guessing an owner would be worse than doing nothing, but
  # an unowned SeaweedFS database must not pass silently.
  export FAKE_CLUSTERS="tenant-manual - -"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "carries no meta.helm.sh/release-name" "$WORK/out"
  rm -rf "$WORK"
}

@test "leaves a Cluster owned by an unrelated release alone" {
  prep
  export FAKE_CLUSTERS="tenant-x some-other-release -"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
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
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "no instance name" "$WORK/out"
  rm -rf "$WORK"
}

# --- 3. fail closed ---------------------------------------------------------

@test "a failing fleet scan aborts the migration instead of stamping past it" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: the Job retries rather than advancing the version.
  [ "$rc" -ne 0 ]
  grep -qF -- "refusing to stamp past an unverified fleet" "$WORK/out"
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

@test "an unreadable owner annotation aborts rather than being read as not-Helm-managed" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_GET_FAIL="Error from server (Forbidden): clusters.postgresql.cnpg.io \"seaweedfs-db\" is forbidden"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "cannot read the Helm owner" "$WORK/out"
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

@test "a failed hand-over aborts rather than stamping a half-migrated fleet" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_ANNOTATE_FAIL="Error from server (Conflict): the object has been modified"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# The fail-open that IS load-bearing: a cluster with no CNPG at all must not be
# blocked from upgrading. "The server doesn't have a resource type" is the only
# list failure allowed to mean "nothing to do".
@test "a cluster with no CNPG resource type stamps cleanly without annotating" {
  prep
  export FAKE_LIST_FAIL="error: the server doesn't have a resource type \"cluster\" in group \"postgresql.cnpg.io\""
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "resource type is not served" "$WORK/out"
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an empty fleet stamps without annotating" {
  prep
  export FAKE_CLUSTERS=""
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'ANNOTATE' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# --- 4. kubeadm bootstrap keep-pin -----------------------------------------
#
# THE THREAT. 1.6 drops KubeadmConfigTemplate from the tenant `kubernetes` chart
# (workers move to TalosConfigTemplate), so its upgrade sees the resource in the
# previous release manifest and absent from the new one and deletes it — while the
# kubeadm-backed MachineSet is still around mid-rollover with its
# bootstrap.configRef pointing at it. 1.6 guards that with its own migration 45.
# A v1.5.4 cluster is stamped 46 and runs `seq 46 53`, so it never executes that
# slot; the pin has to already be on the objects, which is what this half of
# release-1.5's slot 45 does.
#
# So the property under test is the ANNOTATION LANDING ON THE OBJECT, not a
# function being callable. Every assertion below reads the PIN records the fake
# kubectl wrote, and the fake models the label selector rather than ignoring it:
# selecting on meta.helm.sh/release-name (an annotation, and therefore never a
# valid selector) returns no rows, so the classic silent-no-op shape of this bug
# fails these tests instead of passing them.
#
# PIN, not ANNOTATE, is the fake's verb for this half — the SeaweedFS assertions
# above count ANNOTATE lines, and a shared verb would couple the two halves'
# tests to each other.
#
# "DID NOT HAPPEN" IS ASSERTED AS `[ "$(grep -c ...)" -eq 0 ]`, NEVER `! grep -q`.
# POSIX and bash both exempt a !-negated pipeline from errexit — "the -e setting
# shall be ignored ... if the command's return value is being inverted with !" —
# so `! grep -q X file` cannot fail a cozytest test no matter what the file
# contains. It reads like an assertion and is a no-op. Measured, not assumed: with
# the two halves of slot 45 deliberately swapped, a `! grep -q 'PIN '` test stayed
# green while the cmdlog plainly contained the PIN line. The counting form puts the
# result in `[`, whose non-zero status does trip errexit.

@test "pins an unannotated Helm-managed KubeadmConfigTemplate, and stamps 46" {
  prep
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "PIN kubeadmconfigtemplate tenant-root/kubernetes-md0 resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "pins every Helm-managed KubeadmConfigTemplate, each in its own namespace" {
  prep
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm
tenant-root kubernetes-gpu-md1 - Helm
tenant-a other-cluster-md0 - Helm"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "PIN kubeadmconfigtemplate tenant-root/kubernetes-md0 resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "PIN kubeadmconfigtemplate tenant-root/kubernetes-gpu-md1 resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "PIN kubeadmconfigtemplate tenant-a/other-cluster-md0 resource-policy=keep" "$FAKE_CMDLOG"
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 3 ]
  rm -rf "$WORK"
}

# Re-running must not write. The pin reads helm.sh/resource-policy first and skips
# on "keep", so an already-pinned fleet produces no annotate call at all — an
# unconditional `kubectl annotate --overwrite` would still be correct on the
# cluster but would make "no-op" unobservable, and this is the assertion that
# keeps it observable.
@test "idempotent: an already-pinned KubeadmConfigTemplate is skipped without a write" {
  prep
  export FAKE_KCTS="tenant-root kubernetes-md0 keep Helm"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "already carries helm.sh/resource-policy=keep" "$WORK/out"
  grep -qF -- "already-pinned=1" "$WORK/out"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# A cluster with no tenant Kubernetes clusters at all. Zero matching objects is a
# clean run, not a failure: the CRDs are served (CAPI is installed platform-wide)
# and the selector simply matches nothing.
@test "zero Helm-managed kubeadm objects is success, not failure" {
  prep
  export FAKE_KCTS=""
  export FAKE_KCS=""
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "pinned=0 already-pinned=0 failures=0" "$WORK/out"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# The KubeadmConfig children CAPI spawns from the template are owned by their
# Machine, not by Helm: no app.kubernetes.io/managed-by label, so Helm never
# prunes them and the pin must not touch them. Pinning one would leave keep on an
# object whose lifecycle belongs to CAPI. Verified against a live v1.5 stand,
# where the spawned KubeadmConfig carries no managed-by at all.
@test "leaves a CAPI-spawned KubeadmConfig alone: it is not Helm-managed" {
  prep
  export FAKE_KCS="tenant-root kubernetes-md0-lw46d-z2wqn - -"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# The fail-open that IS load-bearing, mirroring the CNPG case above: a cluster
# without the CAPI bootstrap provider has nothing to pin and must not be blocked
# from upgrading.
@test "a cluster with no kubeadm bootstrap provider stamps cleanly without pinning" {
  prep
  export FAKE_KUBEADM_LIST_FAIL="error: the server doesn't have a resource type \"kubeadmconfigtemplates\" in group \"bootstrap.cluster.x-k8s.io\""
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "is not served on this cluster" "$WORK/out"
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing kubeadm fleet scan aborts instead of stamping past it" {
  prep
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  export FAKE_KUBEADM_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # Must propagate: the Job retries rather than advancing the version, because
  # nothing later will pin what this pass could not see.
  [ "$rc" -ne 0 ]
  grep -qF -- "refusing to stamp past an unverified fleet" "$WORK/out"
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# The one per-object failure that is NOT a failure: an app deleted concurrently
# with this hook takes its KubeadmConfigTemplate away between the fleet scan and
# the annotate. Helm cannot prune what no longer exists, so nothing is at risk, and
# failing the pre-upgrade hook over it would block the platform upgrade on an
# object nobody needs. "not found" is accepted HERE and deliberately not for the
# fleet scan, where a list never answers NotFound and accepting it would let a real
# failure read as an empty fleet.
@test "an object that disappears between the scan and the pin is skipped, not fatal" {
  prep
  export FAKE_KCTS="tenant-doomed kubernetes-md0 - Helm"
  export FAKE_KUBEADM_ANNOTATE_FAIL="Error from server (NotFound): kubeadmconfigtemplates.bootstrap.cluster.x-k8s.io \"kubernetes-md0\" not found"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "disappeared between the scan and the pin" "$WORK/out"
  grep -qF -- "pinned=0 already-pinned=1 failures=0" "$WORK/out"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed pin aborts rather than stamping a half-pinned fleet" {
  prep
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  export FAKE_KUBEADM_ANNOTATE_FAIL="Error from server (Conflict): the object has been modified"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "could not be pinned" "$WORK/out"
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# Aggregation, and the reason it is worth having: one unpinnable object must not
# stop the others from being pinned, because the version is not stamped either way
# and the next attempt starts from the same place. The failing namespace is listed
# FIRST so a loop that aborted on the first failure would leave tenant-b unpinned
# and fail this test.
@test "a partial pin failure still pins the rest, then aborts without stamping" {
  prep
  export FAKE_KCTS="tenant-a broken-md0 - Helm
tenant-b healthy-md0 - Helm"
  export FAKE_KUBEADM_ANNOTATE_FAIL="Error from server (Forbidden): kubeadmconfigtemplates.bootstrap.cluster.x-k8s.io is forbidden"
  export FAKE_KUBEADM_ANNOTATE_FAIL_NS="tenant-a"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF -- "PIN kubeadmconfigtemplate tenant-b/healthy-md0 resource-policy=keep" "$FAKE_CMDLOG"
  [ "$(grep -cF -- "tenant-a/broken-md0 resource-policy=keep" "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "pinned=1 already-pinned=0 failures=1" "$WORK/out"
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# --- 5. the two halves, in one slot ----------------------------------------

@test "runs both halves in one pass: SeaweedFS hand-over first, then the pin" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF -- "ANNOTATE tenant-named release-name=foo-db resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "PIN kubeadmconfigtemplate tenant-root/kubernetes-md0 resource-policy=keep" "$FAKE_CMDLOG"
  grep -qF -- "STAMP 46" "$FAKE_CMDLOG"
  # Order is deliberate, not incidental: a missed hand-over loses a tenant's filer
  # metadata, a missed pin gives a recoverable broken worker rollover. On a pass
  # where only one half gets to run, it must be the irreversible one.
  sw_line=$(grep -n 'ANNOTATE tenant-named' "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
  pin_line=$(grep -n 'PIN kubeadmconfigtemplate' "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
  [ "$sw_line" -lt "$pin_line" ]
  rm -rf "$WORK"
}

# The other side of that order: the SeaweedFS half failing must abort before the
# pin is attempted at all, and must not stamp.
@test "a SeaweedFS failure aborts before the pin half runs" {
  prep
  export FAKE_CLUSTERS="tenant-named foo-system -"
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  export FAKE_LIST_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_migration 45 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  [ "$(grep -c 'STAMP' "$FAKE_CMDLOG")" -eq 0 ]
  rm -rf "$WORK"
}

# Migration 43 sources ONLY lib/seaweedfs-db-adopt.sh. The pin must not leak into
# it: a cluster running 43 is mid-upgrade to 44 and 1.6's slot 45 will still run
# for it, and the two libs must stay independently sourceable (neither may call
# the other's private helpers).
@test "migration 43 does not pin: the keep-pin belongs to slot 45 alone" {
  prep
  export FAKE_CLUSTERS="tenant-root seaweedfs-system -"
  export FAKE_KCTS="tenant-root kubernetes-md0 - Helm"
  rc=0
  run_migration 43 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  [ "$(grep -c 'PIN ' "$FAKE_CMDLOG")" -eq 0 ]
  grep -qF -- "STAMP 44" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
