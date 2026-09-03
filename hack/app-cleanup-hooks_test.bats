#!/usr/bin/env bats
# Behavioural tests for the two post-delete cleanup hooks that reclaim storage
# and ACME objects when an app release is uninstalled:
#
#   packages/apps/qdrant/templates/hooks/cleanup-pvc.yaml
#   packages/apps/harbor/templates/hooks/cleanup.yaml
#
# The scripts are extracted from the rendered charts and RUN against a fake
# kubectl, because the property under test is the container's exit code and no
# amount of matching against the manifest text establishes it. The helm-unittest
# suites in packages/apps/*/tests/ match the source text instead.
#
# What is pinned here:
#   * a delete that failed makes the hook exit non-zero. Without that, Helm
#     records the hook as succeeded and the hook-succeeded delete policy removes
#     the Job together with the pod that held the only account of the failure;
#   * one failed delete does not skip the deletes that follow it;
#   * an object that is already gone stays a success, so a re-run over an
#     already-clean namespace exits 0;
#   * a listing that FAILED is distinguishable from one that returned nothing.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. The runner has no setup/teardown, so each
# @test builds its own fixture and removes it at the end of the body. No EXIT
# trap: docs/agents/e2e-testing.md bans them in hack/*.bats, and under the bats
# binary a test that installs one and then fails prints no TAP line at all.

QDRANT_CHART=packages/apps/qdrant
HARBOR_CHART=packages/apps/harbor

# render_hook <chart> <release> <namespace> <job-name> <out> [extra helm args…]
# Extract the named Job's shell script from the rendered chart into <out>. The
# non-empty check must be an early `return 1`: hack/cozytest.sh rewrites every
# line matching ^}$ into `return 0` plus `}`, helpers included, so a check left
# in last-command position has its status discarded.
render_hook() {
  chart=$1 release=$2 namespace=$3 job=$4 out=$5
  shift 5
  helm template "$release" "$chart" --namespace "$namespace" "$@" \
    | yq eval "select(.kind == \"Job\" and .metadata.name == \"$job\") | .spec.template.spec.containers[0].command[2]" - > "$out"
  [ -s "$out" ] || return 1
}

# write_fake_kubectl <dir>
# Emit a fake `kubectl` that logs every call to $KLOG and models the three
# kubectl behaviours these hooks depend on:
#   * a listing prints the fixture held in $PVC_LIST / $CHALLENGE_LIST /
#     $SOLVER_LIST and exits 0 — or, when its kind is named in $FAIL_GET, prints
#     an error and exits 1 having printed nothing, which is what makes "the
#     listing failed" and "the listing was empty" different events;
#   * a delete whose kind is named in $FAIL_DELETE exits 1;
#   * a delete of an object absent from $EXISTING is a NotFound: exit 0 under
#     --ignore-not-found, exit 1 without it. Dropping that flag from a hook
#     therefore turns the already-gone cases red rather than passing silently.
# A label-selector delete matches whatever is present and always exits 0, as
# kubectl's does. No line below is a bare column-0 `}`, so cozytest.sh's parser
# leaves the fixture intact.
write_fake_kubectl() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
verb=$1
kind=${2:-}

# The by-name deletes put the object name last. Flags and the value that
# follows -n / -l are skipped, so what survives is the trailing positional.
obj=""
skip=0
for a in "$@"; do
  if [ "$skip" = 1 ]; then skip=0; continue; fi
  case "$a" in
    -n|-l) skip=1 ;;
    -*) ;;
    *) obj=$a ;;
  esac
done

if [ "$verb" = get ]; then
  case " ${FAIL_GET:-} " in
    *" $kind "*) echo "error: the server could not list $kind" >&2; exit 1 ;;
  esac
  case "$kind" in
    pvc) printf '%s' "${PVC_LIST:-}" ;;
    challenge) printf '%s' "${CHALLENGE_LIST:-}" ;;
    ingress) printf '%s' "${SOLVER_LIST:-}" ;;
    *) echo "fake kubectl: unexpected get kind $kind" >&2; exit 64 ;;
  esac
  exit 0
fi

if [ "$verb" != delete ]; then
  echo "fake kubectl: unexpected verb $verb" >&2
  exit 64
fi

# qdrant deletes through xargs as `delete -n NS --ignore-not-found <name>…`, so
# there is no kind argument on that call; it is always a PVC delete.
case "$kind" in -*) kind=pvc ;; esac

case " ${FAIL_DELETE:-} " in
  *" $kind "*) echo "Error from server: failed to delete $kind $obj" >&2; exit 1 ;;
esac
case "$*" in *" -l "*) exit 0 ;; esac
case " ${EXISTING:-} " in
  *" $obj "*) exit 0 ;;
esac
case "$*" in
  *--ignore-not-found*) exit 0 ;;
esac
echo "Error from server (NotFound): $obj not found" >&2
exit 1
KEOF
  chmod 0755 "$1/kubectl"
}

@test "a render that produces no script fails instead of passing as a no-op" {
  # Guards the guard: every test below runs whatever render_hook produced, so an
  # empty render would satisfy the exit-0 assertions without exercising a line
  # of the hook. This also fails if the emptiness check is ever moved into
  # last-command position, where cozytest.sh's `return 0` rewrite swallows it.
  tmp=$(mktemp -d)
  if render_hook "$tmp/no-such-chart" r ns r-cleanup "$tmp/hook.sh" 2>/dev/null; then
    echo "FAIL: render_hook reported success with no chart to render"
    false
  fi
  if render_hook "$QDRANT_CHART" qdrant-test tenant-test no-such-job "$tmp/hook.sh"; then
    echo "FAIL: render_hook reported success for a Job name the chart never emits"
    false
  fi
  rm -rf "$tmp"
}

@test "qdrant hook exits 0 and deletes exactly the release's data PVCs" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$QDRANT_CHART" qdrant-test tenant-test qdrant-test-qdrant-cleanup "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  # Two of this release's data PVCs, one belonging to the sibling release
  # "qdrant-test-2", and one unrelated claim.
  export PVC_LIST='persistentvolumeclaim/qdrant-storage-qdrant-test-0
persistentvolumeclaim/qdrant-storage-qdrant-test-1
persistentvolumeclaim/qdrant-storage-qdrant-test-2-0
persistentvolumeclaim/db-qdrant-test-0'
  export EXISTING='persistentvolumeclaim/qdrant-storage-qdrant-test-0 persistentvolumeclaim/qdrant-storage-qdrant-test-1'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc when every delete succeeded"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'PVC cleanup complete' "$tmp/out"; then
    echo "FAIL: hook did not report completion"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'delete .*qdrant-storage-qdrant-test-0' "$tmp/calls"; then
    echo "FAIL: the release's data PVCs were never deleted"
    cat "$tmp/calls"
    false
  fi
  if grep -q 'qdrant-storage-qdrant-test-2-0' "$tmp/calls"; then
    echo "FAIL: the sibling release's PVC was included in the delete"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "qdrant hook exits 0 when the data PVCs are already gone" {
  # The idempotent re-run: uninstalling twice, or uninstalling a release whose
  # storage was reclaimed by hand, must not be reported as a failed cleanup.
  # Covered in both shapes it takes — nothing matched the selector at all, and
  # the delete itself came back NotFound.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$QDRANT_CHART" qdrant-test tenant-test qdrant-test-qdrant-cleanup "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export PVC_LIST=''
  export EXISTING=''

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc with no matching PVCs in the namespace"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'nothing to delete' "$tmp/out"; then
    echo "FAIL: hook did not report an empty namespace"
    cat "$tmp/out"
    false
  fi

  # Same release, but the PVCs are listed and disappear before the delete lands.
  : > "$tmp/calls"
  export PVC_LIST='persistentvolumeclaim/qdrant-storage-qdrant-test-0'
  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out2" 2> "$tmp/err2" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc on a NotFound delete, which --ignore-not-found makes a success"
    cat "$tmp/out2" "$tmp/err2"
    false
  fi
  if ! grep -q 'delete .*qdrant-storage-qdrant-test-0' "$tmp/calls"; then
    echo "FAIL: the delete was never attempted, so the NotFound path was not exercised"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "qdrant hook exits non-zero and names the PVCs when the delete fails" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$QDRANT_CHART" qdrant-test tenant-test qdrant-test-qdrant-cleanup "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export PVC_LIST='persistentvolumeclaim/qdrant-storage-qdrant-test-0'
  export EXISTING="$PVC_LIST"
  export FAIL_DELETE='pvc'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although the PVC delete failed"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  # The message must name what was left behind, not merely that something was.
  # Anchored on the hook's own `ERROR: ` prefix: the fake kubectl names the
  # object in its own diagnostic too, so an unanchored match is satisfied by
  # the fake and stays green even when the hook stops naming anything.
  if ! grep -q '^ERROR: .*qdrant-storage-qdrant-test-0' "$tmp/err"; then
    echo "FAIL: the failure was not attributed to a named PVC"
    cat "$tmp/err"
    false
  fi
  if grep -q 'PVC cleanup complete' "$tmp/out"; then
    echo "FAIL: hook reported completion after a failed delete"
    cat "$tmp/out"
    false
  fi
  rm -rf "$tmp"
}

@test "qdrant hook exits non-zero when the PVC listing fails instead of reporting nothing to delete" {
  # A failed listing used to reach the filter as an empty string, so the hook
  # announced an empty namespace and exited 0 without having looked.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$QDRANT_CHART" qdrant-test tenant-test qdrant-test-qdrant-cleanup "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export PVC_LIST=''
  export EXISTING=''
  export FAIL_GET='pvc'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although it never managed to list the PVCs"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if grep -q 'nothing to delete' "$tmp/out"; then
    echo "FAIL: a failed listing was reported as an empty namespace"
    cat "$tmp/out"
    false
  fi
  if ! grep -q 'failed to list PVCs' "$tmp/err"; then
    echo "FAIL: the failed listing was not named"
    cat "$tmp/err"
    false
  fi
  rm -rf "$tmp"
}

@test "harbor hook exits 0 and sweeps PVCs, Certificate and ACME solvers" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST='harbor-test-cert-1 '
  export SOLVER_LIST='cm-acme-http-solver-abcde '
  export EXISTING='harbor-test-ingress-tls harbor-test-cert-1 cm-acme-http-solver-abcde'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc when every delete succeeded"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'Cleanup complete' "$tmp/out"; then
    echo "FAIL: hook did not report completion"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  for expected in 'delete pvc' 'delete certificate' 'delete challenge' 'delete ingress'; do
    if ! grep -q "$expected" "$tmp/calls"; then
      echo "FAIL: the hook never issued: $expected"
      cat "$tmp/calls"
      false
    fi
  done
  rm -rf "$tmp"
}

@test "harbor hook exits 0 when every object is already gone" {
  # No in-flight ACME challenge and no solver Ingress is the ordinary case, and
  # the Certificate is usually collected with the release before the hook runs.
  # All of it must stay a success.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST=''
  export SOLVER_LIST=''
  export EXISTING=''

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc on an already-clean namespace"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  # The Certificate delete is the one that actually reaches a NotFound here, so
  # it has to have been attempted for this test to mean anything.
  if ! grep -q 'delete certificate .*harbor-test-ingress-tls' "$tmp/calls"; then
    echo "FAIL: the Certificate delete was never attempted"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "harbor hook reports a failed delete, names the object and still runs the later sweeps" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST='harbor-test-cert-1 '
  export SOLVER_LIST='cm-acme-http-solver-abcde '
  export EXISTING='harbor-test-ingress-tls harbor-test-cert-1 cm-acme-http-solver-abcde'
  export FAIL_DELETE='pvc'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although the PVC delete failed"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q '^ERROR: .*release=harbor-test-system' "$tmp/err"; then
    echo "FAIL: the failure was not attributed to the objects it left behind"
    cat "$tmp/err"
    false
  fi
  # The first failure must not cost the release its Certificate and solver
  # cleanup: those run to completion and the exit code is decided at the end.
  for expected in 'delete certificate' 'delete challenge' 'delete ingress'; do
    if ! grep -q "$expected" "$tmp/calls"; then
      echo "FAIL: the sweep stopped at the first failure, skipping: $expected"
      cat "$tmp/calls"
      false
    fi
  done
  if grep -q 'Cleanup complete' "$tmp/out"; then
    echo "FAIL: hook reported completion after a failed delete"
    cat "$tmp/out"
    false
  fi
  rm -rf "$tmp"
}

@test "harbor hook reports every ACME delete that failed, each by name" {
  # The PVC sweep is only one of harbor's failure paths; the Certificate and the
  # two per-object ACME deletes are three more, and #3566 names each of them
  # separately. Covered together, with the PVC delete deliberately succeeding,
  # so this test fails if ANY of the three stops reporting — reverting one of
  # them to `|| echo "WARNING …"` is the exact defect being fixed.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST='harbor-test-cert-1 '
  export SOLVER_LIST='cm-acme-http-solver-abcde '
  export EXISTING='harbor-test-ingress-tls harbor-test-cert-1 cm-acme-http-solver-abcde'
  export FAIL_DELETE='certificate challenge ingress'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although three ACME deletes failed"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  # Anchored on the hook's own ERROR: prefix — the fake kubectl names the object
  # in its own diagnostic too, so an unanchored match would be satisfied by the
  # fixture and stay green with the hook reporting nothing.
  for object in 'Certificate tenant-test/harbor-test-ingress-tls' \
                'Challenge tenant-test/harbor-test-cert-1' \
                'solver Ingress tenant-test/cm-acme-http-solver-abcde'; do
    if ! grep -q "^ERROR: .*$object" "$tmp/err"; then
      echo "FAIL: the hook did not report the failed delete of: $object"
      cat "$tmp/err"
      false
    fi
  done
  if grep -q 'Cleanup complete' "$tmp/out"; then
    echo "FAIL: hook reported completion after three failed deletes"
    cat "$tmp/out"
    false
  fi
  rm -rf "$tmp"
}

@test "harbor hook reports a failed solver Ingress listing, the second of its two loops" {
  # The Challenge listing is covered below; this is the other loop. Both have
  # the same shape and each needs its own case, because a revert of one is
  # invisible to a test that only exercises the other.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST='harbor-test-cert-1 '
  export SOLVER_LIST=''
  export EXISTING='harbor-test-ingress-tls harbor-test-cert-1'
  export FAIL_GET='ingress'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although it never managed to list the solver Ingresses"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q '^ERROR: .*failed to list solver Ingresses' "$tmp/err"; then
    echo "FAIL: the failed solver Ingress listing was not reported by the hook"
    cat "$tmp/err"
    false
  fi
  # The Challenge sweep runs before it and must have completed normally: this
  # failure is counted, not fatal.
  if ! grep -q 'delete challenge .*harbor-test-cert-1' "$tmp/calls"; then
    echo "FAIL: the Challenge sweep preceding the failed listing did not run"
    cat "$tmp/calls"
    false
  fi
  if grep -q 'Cleanup complete' "$tmp/out"; then
    echo "FAIL: hook reported completion after a failed listing"
    cat "$tmp/out"
    false
  fi
  rm -rf "$tmp"
}

@test "harbor hook exits non-zero when a listing fails instead of sweeping nothing" {
  # Iterating $(kubectl get …) directly cannot tell a failed listing from an
  # empty one: both leave the loop body unexecuted, so the sweep reported
  # success without having looked.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$HARBOR_CHART" harbor-test tenant-test harbor-test-cleanup "$tmp/hook.sh" \
    --set '_namespace.host=example.org' --set '_cluster.solver=http01'

  export KLOG="$tmp/calls"
  export CHALLENGE_LIST=''
  export SOLVER_LIST='cm-acme-http-solver-abcde '
  export EXISTING='harbor-test-ingress-tls cm-acme-http-solver-abcde'
  export FAIL_GET='challenge'

  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 although it never managed to list the Challenges"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'failed to list Challenges' "$tmp/err"; then
    echo "FAIL: the failed listing was not named"
    cat "$tmp/err"
    false
  fi
  # A failed listing is counted, not fatal: the solver Ingress sweep after it
  # still runs and still deletes what it finds.
  if ! grep -q 'delete ingress .*cm-acme-http-solver-abcde' "$tmp/calls"; then
    echo "FAIL: the failed listing aborted the solver Ingress sweep that follows it"
    cat "$tmp/calls"
    false
  fi
  if grep -q 'Cleanup complete' "$tmp/out"; then
    echo "FAIL: hook reported completion after a failed listing"
    cat "$tmp/out"
    false
  fi
  rm -rf "$tmp"
}
