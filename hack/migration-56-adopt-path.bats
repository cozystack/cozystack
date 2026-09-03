#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the ADOPTION path of platform migration 56.
#
# migration-56-adopt-values.bats pins the jq value-mapping; this file pins the
# adopt_one branch that mutates ownership of running worker objects (annotate
# helm.sh/resource-policy=keep + meta.helm.sh/release-name). The fake kubectl
# serves worker objects via FAKE_OBJS ("<res> <name> <release-name>" lines) and
# KubevirtMachineTemplate names via FAKE_KMT_NAMES, so the annotate, idempotency
# and refusal branches and the 6-hex KMT anchor are exercised directly rather
# than short-circuited on "absent". FAKE_GET_FAIL injects transient server
# errors and FAKE_WARN_STDERR injects success-path stderr warnings, pinning the
# read contract: absence (--ignore-not-found success + empty output) is
# skipped, ANY read failure aborts the run before the version stamp, and
# stderr never contaminates data.
#
# cozytest.sh awk parser: @test blocks only, a bare `}` at column 0 ends a test,
# no run/$status/setup/teardown. Assertions are direct shell tests.
# Run with: hack/cozytest.sh hack/migration-56-adopt-path.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/migration-56"
MIG="$PWD/packages/core/platform/images/migrations/migrations/56"

prep() {
  chmod +x "$FAKEBIN/kubectl"
  WORK=$(mktemp -d)
  export FAKE_HR_LIST="$WORK/hrlist.json"
  export FAKE_CHILD_HR="$WORK/child-hr.json"
  export FAKE_CMDLOG="$WORK/cmdlog"
  export FAKE_OBJS="$WORK/objs"
  : > "$FAKE_CMDLOG"
  export PATH="$FAKEBIN:$PATH"
  export NAMESPACE=cozy-system
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"md0":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
}

@test "adopt_one pins keep + child release-name on parent-owned worker objects, anchoring the KMT match" {
  prep
  # Worker objects currently owned by the parent release, plus a sibling pool's
  # KMT (md0-large) the 6-hex anchor must NOT adopt while processing md0.
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-abc123 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-large-def456 kubernetes-test3
OBJS
  FAKE_KMT_NAMES=$(printf '%s\n%s' kubernetes-test3-md0-abc123 kubernetes-test3-md0-large-def456)
  export FAKE_KMT_NAMES
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  # MD/MHC/WM adopted onto the child release with keep.
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate machinedeployment.* meta.helm.sh/release-name=kubernetes-nodes-test3-md0' "$FAKE_CMDLOG"
  grep -qE 'annotate machinehealthcheck.* kubernetes-test3-md0 ' "$FAKE_CMDLOG"
  grep -qE 'annotate workloadmonitor.* kubernetes-test3-md0 ' "$FAKE_CMDLOG"
  # The pool's own KMT (6-hex suffix) is adopted...
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-test3-md0-abc123 ' "$FAKE_CMDLOG"
  # ...but the sibling pool md0-large's KMT is NOT (anchor stops the mis-adopt).
  ! grep -qE 'kubernetes-test3-md0-large-def456' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "adopt_one is idempotent: objects already on the child release are skipped" {
  prep
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-nodes-test3-md0
OBJS
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  # Already adopted -> no annotate issued at all.
  ! grep -qE 'annotate ' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "adopt_one pins keep but does not adopt an object owned by an unexpected release (no prune, no deadlock)" {
  prep
  # A worker object whose meta.helm.sh/release-name was hand-edited to a third
  # release (reachable via `kubectl annotate` on the tenant namespace) must NOT
  # abort the whole pre-upgrade hook: exiting 1 under set -e would deadlock the
  # platform upgrade for EVERY tenant on one tenant's corrupted annotation, the
  # same fleet-wide deadlock the non-object / overflow / invalid-name branches
  # were reshaped to avoid. But it must still be PINNED: the object is in the
  # parent's old pre-split manifest, so the control-plane-only re-render prunes
  # it unless resource-policy=keep is set -- Helm's prune gates on that
  # annotation, not on the live release-name. Refuse to adopt THIS object
  # (rewriting a foreign release-name could corrupt it), pin keep, warn, and let
  # the run complete and stamp. The sibling parent-owned objects still adopt.
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 some-other-release
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
OBJS
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'owned by unexpected release' "$WORK/out"
  # The foreign-owned MD is pinned with keep so the parent cannot prune it...
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  # ...but it is never re-annotated onto the child release (ownership untouched).
  ! grep -qE 'annotate machinedeployment.* meta.helm.sh/release-name=kubernetes-nodes-test3-md0' "$FAKE_CMDLOG"
  # The sibling parent-owned MHC still adopts, and the run stamps.
  grep -qE 'annotate machinehealthcheck.* meta.helm.sh/release-name=kubernetes-nodes-test3-md0' "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  # The foreign-owned object is a pinned-but-unadopted case too, so it must land
  # in the durable unadopted record (not only the reaped Job log): the child HR
  # can stay Failed on it and an operator needs to find it after the fact.
  grep -qF -- "APPLY-UNADOPTED-CM" "$FAKE_CMDLOG"
  grep -qiE 'pinned prune-proof but NOT adopted' "$WORK/out"
  rm -rf "$WORK"
}

@test "an RFC-1123-invalid nodeGroup key is pinned and skipped, not applied (no upgrade deadlock)" {
  prep
  # The old nodeGroups map key was never schema-constrained, so a parent HR can
  # carry a key like 'My_Pool' that yields the RFC-1123-invalid child HR name
  # kubernetes-nodes-test3-My_Pool. Without the guard the create path reaches
  # `kubectl apply`, the fake apiserver rejects the name, and the migration exits
  # 1 -- deadlocking every tenant's platform pre-upgrade hook. The guard must
  # warn, pin the (absent) pool objects as a no-op, skip adoption, and continue.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"My_Pool":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  # Guard caught it: the run succeeds instead of deadlocking.
  [ "$rc" -eq 0 ]
  grep -qiE 'not a valid RFC-1123 label' "$WORK/out"
  # The invalid child HelmRelease is never applied.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-object nodeGroup value is pinned and skipped, not crashed on (no upgrade deadlock)" {
  prep
  # kubectl edit on the parent HR bypasses the aggregated API's schema, so a group
  # value can be stored as a non-object (here a string). The value-mapping jq then
  # runs `"oops" | has(...)`, which aborts with exit 5 under set -e and deadlocks the
  # platform pre-upgrade hook for every tenant. The normalize-to-null guard must warn,
  # pin the (absent) pool objects, skip adoption, and let the run complete.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"md0":"oops"}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'non-object value' "$WORK/out"
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-object nodeGroups map is skipped per-cluster, not crashed on (no upgrade deadlock)" {
  prep
  # nodeGroups itself stored as a non-object (here an array). keys[] on an array
  # yields indices, then .[$idx] fails with "Cannot index array with string", exiting
  # 1 under set -e and deadlocking every tenant's pre-upgrade hook. The shape guard
  # must warn and skip this cluster's pool adoption, letting the run complete.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":["md0"]}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'not an object' "$WORK/out"
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-object nodeGroups map still pins the cluster's live worker pools before skipping (no prune)" {
  prep
  # nodeGroups mangled out-of-band to a non-object (array), but the cluster still
  # has live worker objects from its last good render. The declared pool keys are
  # unreadable, so the migration discovers the live pools from their KMTs and pins
  # every worker object with resource-policy=keep -- otherwise the parent's
  # control-plane-only re-render prunes the un-pinned VMs. No child HR is created
  # (adoption is skipped), but the run completes and stamps. Two pools (md0, md1)
  # prove discovery is not limited to the implicit md0.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":["md0"]}}}]}
JSON
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-abc123 kubernetes-test3
machinedeployment.cluster.x-k8s.io kubernetes-test3-md1 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md1-def456 kubernetes-test3
OBJS
  FAKE_KMT_NAMES=$(printf '%s\n%s' kubernetes-test3-md0-abc123 kubernetes-test3-md1-def456)
  export FAKE_KMT_NAMES
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'not an object' "$WORK/out"
  # Both discovered pools pinned with keep, no ownership transfer (no release-name).
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-test3-md0-abc123 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md1 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-test3-md1-def456 .*resource-policy=keep' "$FAKE_CMDLOG"
  ! grep -qE 'meta.helm.sh/release-name' "$FAKE_CMDLOG"
  # Adoption skipped, run completes.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-scalar value for a scalar nodeGroup field pins the live pool objects before skipping (no prune)" {
  prep
  # The group key and its map value are both well-formed, but a scalar field
  # (here storageClass) is stored as an object -- reachable via kubectl edit,
  # since the NodeGroup subfields were never fully schema-constrained. Copied
  # verbatim into the child HR it would fail the KubernetesNodes schema and
  # leave the child release stuck Failed, with the MD already re-annotated onto
  # it. The bad-scalar guard must instead warn, pin the live pool objects with
  # resource-policy=keep (so the parent's control-plane-only re-render cannot
  # prune the running VMs), skip adoption (no child HR), and let the run stamp.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"md0":{"minReplicas":1,"storageClass":{"name":"replicated"},"roles":["ingress-nginx"]}}}}}]}
JSON
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-test3-md0-abc123 kubernetes-test3
OBJS
  FAKE_KMT_NAMES=$(printf '%s' kubernetes-test3-md0-abc123)
  export FAKE_KMT_NAMES
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'non-scalar value' "$WORK/out"
  # Live pool objects pinned with keep, no ownership transfer (no release-name).
  grep -qE 'annotate machinedeployment.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate machinehealthcheck.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate workloadmonitor.* kubernetes-test3-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-test3-md0-abc123 .*resource-policy=keep' "$FAKE_CMDLOG"
  ! grep -qE 'meta.helm.sh/release-name' "$FAKE_CMDLOG"
  # Adoption skipped, run completes and stamps.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a child release name over 53 chars pins the live pool objects and skips, not deadlock on kubectl apply (no upgrade deadlock)" {
  prep
  # rest.go caps a cluster name at 42 chars, and the kubernetes-nodes- prefix
  # (17 chars) plus a pool name can push the child HelmRelease name past Helm's
  # 53-char limit. Here cluster 'production-eu-central-analytics01' (33) + pool
  # 'md0' yields child name 'kubernetes-nodes-production-eu-central-analytics01-md0'
  # (54 chars). Without the length guard the flow reaches `kubectl apply` of a
  # >53-char HelmRelease name, the apiserver rejects it, and under set -e the
  # run exits 1 before stamping -- deadlocking every tenant's platform upgrade.
  # The overflow branch must instead warn, pin the live pool objects prune-proof,
  # skip adoption (no child HR), and let the run complete and stamp.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-production-eu-central-analytics01"},"spec":{"values":{"nodeGroups":{"md0":{"minReplicas":1,"roles":["ingress-nginx"]}}}}}]}
JSON
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-production-eu-central-analytics01-md0 kubernetes-production-eu-central-analytics01
machinehealthcheck.cluster.x-k8s.io kubernetes-production-eu-central-analytics01-md0 kubernetes-production-eu-central-analytics01
workloadmonitor.cozystack.io kubernetes-production-eu-central-analytics01-md0 kubernetes-production-eu-central-analytics01
kubevirtmachinetemplate.infrastructure.cluster.x-k8s.io kubernetes-production-eu-central-analytics01-md0-abc123 kubernetes-production-eu-central-analytics01
OBJS
  FAKE_KMT_NAMES=$(printf '%s' kubernetes-production-eu-central-analytics01-md0-abc123)
  export FAKE_KMT_NAMES
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE "exceeds Helm's 53-char limit" "$WORK/out"
  # Live pool objects pinned with keep, no ownership transfer (no release-name).
  grep -qE 'annotate machinedeployment.* kubernetes-production-eu-central-analytics01-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate machinehealthcheck.* kubernetes-production-eu-central-analytics01-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate workloadmonitor.* kubernetes-production-eu-central-analytics01-md0 .*resource-policy=keep' "$FAKE_CMDLOG"
  grep -qE 'annotate kubevirtmachinetemplate.* kubernetes-production-eu-central-analytics01-md0-abc123 .*resource-policy=keep' "$FAKE_CMDLOG"
  ! grep -qE 'meta.helm.sh/release-name' "$FAKE_CMDLOG"
  # Adoption skipped (no child HR applied), run completes and stamps.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "an absent worker object is skipped and the run still completes and stamps" {
  prep
  # No FAKE_OBJS: every named get is absent -- under --ignore-not-found that is
  # SUCCESS with empty output, the way real kubectl behaves. adopt_one must
  # classify that as absent (skip) -- this pins the absent path so the
  # fail-closed tests below prove classification, not blanket failure.
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -q 'absent in tenant-test, skipping' "$WORK/out"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a transient error reading a worker object fails the run closed, before the version stamp" {
  prep
  # kubectl exits 1 for NotFound and for a transient server failure alike. If a
  # timeout were classified as "absent", adoption would be skipped WITHOUT the
  # keep pin, the migration would complete and stamp 57 (never re-running), and
  # the parent's control-plane-only upgrade would prune the live pool's
  # MachineDeployment -- deleting every running worker VM. The migration must
  # instead exit non-zero with no stamp, so run-migrations.sh retries it.
  export FAKE_GET_FAIL="machinedeployment"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  grep -q 'ERROR: reading machinedeployment' "$WORK/out"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_GET_FAIL
  rm -rf "$WORK"
}

@test "a transient error listing KubevirtMachineTemplates fails the run closed, before the version stamp" {
  prep
  # A failed KMT listing must not read as "no templates": zero KMTs would be
  # adopted, the migration would stamp, and the parent upgrade would prune a
  # preserved older-revision KMT still referenced by an in-flight MachineSet --
  # permanently, since the child chart only re-emits KMTs already annotated with
  # its own release name. MD/MHC/WM are present and adoptable so the run
  # provably fails at the listing, not earlier.
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
OBJS
  export FAKE_GET_FAIL="kubevirtmachinetemplate"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  grep -q 'ERROR: listing KubevirtMachineTemplates' "$WORK/out"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_GET_FAIL
  rm -rf "$WORK"
}

@test "a transient error on the pin path (unadoptable pool) fails the run closed, before the version stamp" {
  prep
  # pin_keep guards pools that cannot be adopted (invalid/overflowing name); a
  # transient read error masked as "absent" there would leave the pool un-pinned
  # for the parent's prune with no child release to notice. Same contract as
  # adopt_one: read failures abort the run. The key is dotted -- label-invalid,
  # so the pin path fires, yet a valid object name, so objects can exist and
  # the pin path genuinely reads; a key that cannot name an object at all is
  # skipped before any read (covered by the slash test above).
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"a.b":{"minReplicas":1}}}}}]}
JSON
  export FAKE_GET_FAIL="machinedeployment"
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  grep -q 'ERROR: reading machinedeployment' "$WORK/out"
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_GET_FAIL
  rm -rf "$WORK"
}

@test "a nodeGroup key containing a slash is skipped without a read, not deadlocked on kubectl's client-side rejection" {
  prep
  # A composed object name with a '/' is rejected by real kubectl client-side,
  # deterministically, before any API call -- so if the pin path issued a read
  # for it, the fail-closed classification would abort the run on every retry,
  # deadlocking the upgrade over a pool that provably cannot exist (the old
  # chart's apply of such a name would have been rejected by the apiserver).
  # The pin path must skip such names statically: nothing to pin, no read made.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"pool/a":{"minReplicas":1}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'not a valid RFC-1123 label' "$WORK/out"
  grep -qi 'nothing to pin' "$WORK/out"
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a kubectl warning on stderr does not contaminate the owner read or break adoption" {
  prep
  # kubectl writes warnings to stderr on SUCCESSFUL reads: apiserver deprecation
  # headers (deterministic, printed on every get of a deprecated GVK -- CAPI
  # v1beta1 qualifies) and client-side throttling notices. If the owner read
  # captured stderr into its data (2>&1), the warning would glue onto the
  # annotation value, the foreign-owner guard would see an unexpected "owner",
  # refuse, and exit 1 -- permanently deadlocking the pre-upgrade hook for every
  # tenant on a warning that never goes away. Adoption must proceed normally.
  cat > "$FAKE_OBJS" <<'OBJS'
machinedeployment.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
machinehealthcheck.cluster.x-k8s.io kubernetes-test3-md0 kubernetes-test3
workloadmonitor.cozystack.io kubernetes-test3-md0 kubernetes-test3
OBJS
  export FAKE_WARN_STDERR=1
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qE 'annotate machinedeployment.* meta.helm.sh/release-name=kubernetes-nodes-test3-md0' "$FAKE_CMDLOG"
  ! grep -qi 'refusing' "$WORK/out"
  grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_WARN_STDERR
  rm -rf "$WORK"
}

@test "a transient error whose message contains 'not found' still fails the run closed" {
  prep
  # A conversion-webhook outage mid-upgrade fails a read with e.g.
  # 'conversion webhook ... failed: service "capi-webhook-service" not found'.
  # That is a transient failure, not absence: classifying it as absent would
  # skip the keep pin, stamp 57, and let the parent upgrade prune the live
  # pool. Only real absence (--ignore-not-found success + empty output) may be
  # skipped; every read failure aborts, whatever its message says.
  export FAKE_GET_FAIL="machinedeployment"
  export FAKE_GET_FAIL_MSG='Error from server: conversion webhook for cluster.x-k8s.io/v1beta1, Kind=MachineDeployment failed: service "capi-webhook-service" not found'
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -ne 0 ]
  ! grep -qF -- "STAMP" "$FAKE_CMDLOG"
  unset FAKE_GET_FAIL FAKE_GET_FAIL_MSG
  rm -rf "$WORK"
}

@test "a whitespace nodeGroup key reaches the RFC-1123 guard intact, not word-split into bogus pools" {
  prep
  # The old nodeGroups map key was never schema-constrained. An unquoted
  # `for group in $(... keys[])` word-splits a key like 'my pool' into 'my' and
  # 'pool' BEFORE the RFC-1123 guard, each fragment individually valid, fabricating
  # two garbage child HelmReleases for a pool that never existed. Newline-safe
  # iteration keeps the key intact so the guard rejects the space and pins + skips.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"my pool":{"minReplicas":1}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'not a valid RFC-1123 label' "$WORK/out"
  # Neither 'my' nor 'pool' is ever applied as a child release.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a non-scalar scalar-field on a nodeGroup is pinned and skipped, not applied to a child that cannot validate" {
  prep
  # kubectl edit on the parent HR bypasses the aggregated API schema, so a field
  # the child HR expects to be scalar (here storageClass) can be stored as an
  # object. Copied verbatim into the child values it fails the KubernetesNodes
  # schema on install, leaving the child release stuck Failed. The scalar-type
  # guard must warn, pin the pool's (absent here) objects, skip adoption, and
  # let the run complete and stamp instead of fabricating an unvalidatable child.
  cat > "$FAKE_HR_LIST" <<'JSON'
{"items":[{"metadata":{"namespace":"tenant-test","name":"kubernetes-test3"},"spec":{"values":{"nodeGroups":{"md0":{"minReplicas":1,"storageClass":{"nested":"oops"}}}}}}]}
JSON
  rc=0
  bash "$MIG" >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"
  [ "$rc" -eq 0 ]
  grep -qiE 'non-scalar value' "$WORK/out"
  grep -q 'storageClass' "$WORK/out"
  # The pool is pinned+skipped, never applied as a child release.
  ! grep -q 'APPLY-HR' "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
