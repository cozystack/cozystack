#!/usr/bin/env bats
# Behavioural tests for the pre-delete cleanup hook of packages/apps/kubernetes:
# the script is extracted from the rendered Job and executed against a stub
# kubectl. The helm-unittest cases in
# packages/apps/kubernetes/tests/delete_hook_test.yaml match its source TEXT,
# which cannot say whether the closing message is reachable once a backstop has
# swallowed a failure.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. The runner has no setup/teardown, so each
# @test builds its own fixture and removes it at the end of the body. No EXIT
# traps: a test that dies inside one prints neither `not ok` nor a reason, and
# the suite reads green. The removal is therefore reached only on a passing run;
# a failing one leaves its temp directory behind, which is what you want when
# reading the rendered script and the captured output afterwards.

CHART=packages/apps/kubernetes

# The stub is driven by two independent lists of globs, matched against the
# joined arguments, and keeping them separate is the point: the script under
# test distinguishes "this object is not there" from "I could not ask", so a
# fixture that spelled both as one failing call could not tell whether an
# assertion passed for the right reason.
#
#   $KUBECTL_FAIL    -- these invocations exit non-zero: the apiserver refused,
#                       timed out, or was unreachable.
#   $KUBECTL_PRESENT -- these `get ... -o name` invocations print an object.
#                       Anything not listed exits 0 with no output, which is
#                       what `--ignore-not-found` does for an absent object.

# The ordinary end state after Step 4 waited: the TenantControlPlane is gone and
# the datastore Secret is still there for Step 4b to clear.
PRESENT_SECRET_ONLY='*get secret*'

# Step 4's delete timed out with the TenantControlPlane still alive, so Step 4b
# must decline to touch a finalizer Kamaji may still own.
FAIL_TCP_DELETE='*delete tenantcontrolplanes*'
PRESENT_TCP_AND_SECRET='*get tenantcontrolplanes*
*get secret*'

# The Secret is there, and the patch that would clear its finalizer is rejected.
FAIL_SECRET_PATCH='*patch secret*'

# Step 2's bounded delete times out, so the run takes the force-clear route --
# and every force-clear then succeeds. A changed route is not residue: nothing
# survives this run, so it must still be reported as a clean one.
FAIL_HR_DELETE='*delete helmreleases*'

# ... and the re-list driving that force-clear fails too. The loop body never
# runs, so no patch is attempted and no per-object backstop can fire.
FAIL_HR_DELETE_AND_LIST='*delete helmreleases*
*get helmreleases*'

# Each of Step 4b's two probes, failing on its own. Neither says the object is
# absent -- they say the script could not find out, which is a third state.
FAIL_TCP_PROBE='*get tenantcontrolplanes*'
FAIL_SECRET_PROBE='*get secret*'

# Every failure at once. The two HelmRelease globs are distinguished by the
# patch body: Step 1 suspends (no "finalizers" in its payload) and must keep
# working, because it is not backstopped and `set -e` would end the run there.
FAIL_ALL='*delete helmreleases*
*patch helmrelease*finalizers*
*delete tenantcontrolplanes*
*get tenantcontrolplanes*
*patch secret*'

# Extract the pre-delete Job's script from the rendered chart into $1. The
# non-empty check must be an early `return 1`: hack/cozytest.sh rewrites every
# line matching ^}$ into `return 0` plus `}`, helpers included, so a check left
# in last position has its status discarded.
#
# helm's stderr is kept rather than discarded, because it carries the reason a
# render failed and without it this returns 1 with nothing to read. Only the
# per-render notice about the charts/cozy-lib symlink is dropped, and only when
# there is a failure to report.
render_script() {
  helm template test-k8s "$CHART" \
    --namespace tenant-test \
    --values "$CHART/tests/values-ci.yaml" \
    --show-only templates/delete.yaml 2>"$1.err" |
    yq 'select(.kind == "Job") | .spec.template.spec.containers[0].command[2]' - > "$1"
  if [ ! -s "$1" ]; then
    echo "FAIL: the chart rendered no pre-delete Job script" >&2
    grep -v 'found symbolic link in path' "$1.err" >&2 || true
    return 1
  fi
}

# A stub kubectl for $1/kubectl. `get ... -o name` answers for the three object
# kinds the script probes; the HelmRelease list returns TWO children so that a
# per-object failure in the force-clear loop hits the same backstop label twice
# and the once-per-step property has something to be true about.
#
# The closing brace of the helper inside the heredoc is indented on purpose:
# hack/cozytest.sh rewrites every line matching ^}$ anywhere in this file, the
# heredoc included, and a `return 0` spliced into the stub would make every
# injected failure disappear.
make_kubectl() {
  mkdir -p "$1"
  cat > "$1/kubectl" <<'STUB'
#!/bin/sh
args="$*"
echo "$args" >> "${KUBECTL_LOG:-/dev/null}"

matches() {
  [ -n "$1" ] || return 1
  oldifs=$IFS
  IFS='
'
  for pat in $1; do
    case "$args" in
      $pat) IFS=$oldifs; return 0 ;;
    esac
  done
  IFS=$oldifs
  return 1
  }

if matches "${KUBECTL_FAIL:-}"; then
  echo "stub kubectl: injected failure: $args" >&2
  exit 1
fi

case "$args" in
  *"get helmreleases"*"-o name"*)
    echo "helmrelease.helm.toolkit.fluxcd.io/cilium"
    echo "helmrelease.helm.toolkit.fluxcd.io/coredns"
    ;;
  *"get tenantcontrolplanes"*"-o name"*)
    matches "${KUBECTL_PRESENT:-}" && echo "tenantcontrolplane.kamaji.clastix.io/test-k8s"
    ;;
  *"get secret"*"-o name"*)
    matches "${KUBECTL_PRESENT:-}" && echo "secret/test-k8s-datastore-config"
    ;;
esac
exit 0
STUB
  chmod 0755 "$1/kubectl"
}

# Fixture: $tmp holds the rendered script and the stub kubectl.
setup_case() {
  tmp=$(mktemp -d) || return 1
  render_script "$tmp/s.sh" || return 1
  make_kubectl "$tmp/bin" || return 1
}

# Run the hook with the injected failures in $1 and the existing objects in $2,
# leaving merged output in $tmp/out and the exit status in $rc. The status must
# not abort the test: it is itself one of the things under test. KUBECONFIG is
# neutered so that a stub that stopped being found on PATH cannot reach a real
# cluster.
run_hook() {
  rc=0
  # Truncated per call, not appended to: the "stub was reached" check below is
  # vacuous on a second run against a log the first run already filled.
  : > "$tmp/kubectl.log"
  KUBECONFIG=/dev/null \
  KUBECTL_LOG="$tmp/kubectl.log" \
  KUBECTL_FAIL="$1" \
  KUBECTL_PRESENT="$2" \
  PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" > "$tmp/out" 2>&1 || rc=$?
  # The stub has to have been reached, or the assertions below measured
  # something other than this script driving kubectl.
  [ -s "$tmp/kubectl.log" ] || return 1
}

@test "claims success only when nothing was left behind" {
  setup_case
  run_hook "" "$PRESENT_SECRET_ONLY"

  [ "$rc" = 0 ]
  grep -qF 'Cleanup completed successfully' "$tmp/out"
  # The clean run must exercise the Step 4b patch rather than skip it, or the
  # success claim is about a script that did almost nothing.
  grep -qF 'Removing finalizers from secret/test-k8s-datastore-config' "$tmp/out"
  if grep -qF 'residue left by' "$tmp/out"; then
    echo "FAIL: a clean run reported residue"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'WARNING' "$tmp/out"; then
    echo "FAIL: a clean run emitted a warning"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a rejected finalizer patch withdraws the success claim and names the step" {
  setup_case
  run_hook "$FAIL_SECRET_PATCH" "$PRESENT_SECRET_ONLY"

  [ "$rc" = 0 ]
  # The defect this pins: the closing line used to assert success even here.
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success while the datastore-secret finalizer was left in place"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'Cleanup incomplete; residue left by: step 4b (datastore-secret finalizer)' "$tmp/out"
  # And only that step: a summary that names every backstop unconditionally
  # tells an operator no more than the old unconditional success line did.
  if grep -qF 'step 2 (' "$tmp/out"; then
    echo "FAIL: a step 2 backstop was reported without firing"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a fallback route that succeeded is reported as a clean run" {
  # Step 2's bounded delete times out and every force-clear that follows works,
  # which is the ordinary outcome for a tenant with no working nodes -- the case
  # the wait is bounded for in the first place. Nothing survives the run, so
  # calling it incomplete would send an operator hunting for objects that are
  # not there: the same class of untrue closing line this file exists to stop,
  # only pointing the other way.
  setup_case
  run_hook "$FAIL_HR_DELETE" "$PRESENT_SECRET_ONLY"

  [ "$rc" = 0 ]
  # The route really was taken, or this passes for a run that never fell back.
  grep -qF 'Force-clearing finalizers on helmrelease.helm.toolkit.fluxcd.io/cilium' "$tmp/out"
  if grep -qF 'residue left by' "$tmp/out"; then
    echo "FAIL: a fallback that left nothing behind was reported as incomplete"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'Cleanup completed successfully' "$tmp/out"

  rm -rf "$tmp"
}

@test "an enumeration that failed is reported, not taken for an empty one" {
  setup_case
  run_hook "$FAIL_HR_DELETE_AND_LIST" "$PRESENT_SECRET_ONLY"

  [ "$rc" = 0 ]
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success after it could not list what it had to force-clear"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'Cleanup incomplete; residue left by: step 2 (child HelmRelease enumeration)' "$tmp/out"
  # And not the per-object label: that one means a patch was attempted and
  # rejected. Here the loop never ran, so claiming it would invent a failure.
  if grep -qF 'step 2 (child HelmRelease finalizers)' "$tmp/out"; then
    echo "FAIL: a per-object patch failure was reported although the loop never ran"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a TenantControlPlane outliving Step 4 is reported even though no command failed" {
  # Step 4 times out with the TCP still alive, so Step 4b declines to strip a
  # finalizer Kamaji may still own. Every kubectl call in Step 4b succeeds, so
  # nothing is caught by a `||` -- and the namespace is still wedged. This is
  # the one residue the script reports from a branch rather than from a failure.
  setup_case
  run_hook "$FAIL_TCP_DELETE" "$PRESENT_TCP_AND_SECRET"

  [ "$rc" = 0 ]
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success with the datastore-secret finalizer left in place"
    cat "$tmp/out"
    return 1
  fi
  # The label names the TenantControlPlane, not the Secret. Both branches of
  # Step 4b leave the finalizer in place, but only the other one wants it
  # cleared by hand; here it is Kamaji's, and a summary pointing at the Secret
  # would send the reader to strip a finalizer its owner is still using.
  grep -qF 'Cleanup incomplete; residue left by: step 4b (TenantControlPlane still deleting)' "$tmp/out"
  if grep -qF 'step 4b (datastore-secret finalizer)' "$tmp/out"; then
    echo "FAIL: the summary blamed the Secret for a TenantControlPlane that is still deleting"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'is still present, so Kamaji may still own' "$tmp/out"

  rm -rf "$tmp"
}

@test "a TenantControlPlane probe that failed is not read as absent" {
  # The probe is the gate that stops the finalizer being stripped from an owner
  # that still exists, so reading its failure as "confirmed gone" defeats it in
  # the one direction that does damage. The script must neither strip nor claim
  # the run was clean.
  setup_case
  run_hook "$FAIL_TCP_PROBE" ""

  [ "$rc" = 0 ]
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success without establishing whether the TenantControlPlane was gone"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'Cleanup incomplete; residue left by: step 4b (datastore-secret state unknown)' "$tmp/out"
  # And it must NOT have gone on to patch: that is the damage the gate prevents.
  if grep -qF 'Removing finalizers from secret/' "$tmp/out"; then
    echo "FAIL: the finalizer was stripped although the TenantControlPlane state was unknown"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a Secret probe that failed is not read as absent" {
  # The TCP is confirmed gone, so Step 4b is entitled to act -- but it cannot
  # find out whether the Secret is there. "not present; nothing to do" would be
  # an assertion the script never checked.
  setup_case
  run_hook "$FAIL_SECRET_PROBE" ""

  [ "$rc" = 0 ]
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success without establishing whether the Secret was still there"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'not present; nothing to do' "$tmp/out"; then
    echo "FAIL: a failed probe was reported as the Secret being absent"
    cat "$tmp/out"
    return 1
  fi
  grep -qF 'Cleanup incomplete; residue left by: step 4b (datastore-secret state unknown)' "$tmp/out"

  rm -rf "$tmp"
}

@test "every step that left residue is named in the closing message" {
  setup_case
  run_hook "$FAIL_ALL" ""

  [ "$rc" = 0 ]
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success with every remedy having failed"
    cat "$tmp/out"
    return 1
  fi
  # Read the summary as one line: asserting the labels against the whole output
  # would pass on the individual WARNINGs, which were already there. Two labels,
  # not five: the timeouts that changed route left nothing of their own, and
  # what survived them is what the per-object failures below record.
  summary=$(grep -F 'residue left by:' "$tmp/out")
  for label in \
    'step 2 (child HelmRelease finalizers)' \
    'step 4b (datastore-secret state unknown)'; do
    case "$summary" in
      *"$label"*) ;;
      *)
        echo "FAIL: the closing message did not name: $label"
        echo "$summary"
        return 1
        ;;
    esac
  done
  # Once each. The force-clear loop fails on both child HelmReleases, and a
  # summary that listed its step twice would read as two separate failures.
  dupes=$(printf '%s\n' "$summary" | tr ',' '\n' |
    grep -cF 'step 2 (child HelmRelease finalizers)' || true)
  if [ "$dupes" != 1 ]; then
    echo "FAIL: the step 2 finalizer backstop is named $dupes times, expected once"
    echo "$summary"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a step with no backstop ends the run instead of claiming success" {
  # The closing message is honest only while the steps that are NOT backstopped
  # still end the run under `set -e`. Nothing but `set -e` enforces that, so a
  # `|| true` appended to one of them later would reinstate the exact defect
  # this file pins: a cleanup that did not happen, reported as success. Step 3
  # stands in for Steps 3, 5, 6 and the patch inside Step 1. Step 1's
  # enumeration is NOT among them: it carries `|| true` on purpose, because
  # Step 2 deletes by label rather than from that list, so a failure there
  # leaves nothing behind to report.
  setup_case
  run_hook '*delete kamajicontrolplanes*' "$PRESENT_SECRET_ONLY"

  if [ "$rc" = 0 ]; then
    echo "FAIL: a failed Step 3 did not end the run"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: cleanup claimed success after Step 3 failed"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "exits 0 whether cleanup was clean or every remedy failed" {
  # Pinned on its own because it is the constraint the honesty fix must not buy
  # its honesty with. This is a pre-delete hook: a non-zero exit aborts the Helm
  # uninstall before the release is removed, so the release stays in storage and
  # the same teardown is retried forever. Reporting failure through the exit
  # code is what the closing message exists to avoid.
  setup_case

  run_hook "" "$PRESENT_SECRET_ONLY"
  if [ "$rc" != 0 ]; then
    echo "FAIL: a clean run exited $rc"
    cat "$tmp/out"
    return 1
  fi

  run_hook "$FAIL_ALL" ""
  if [ "$rc" != 0 ]; then
    echo "FAIL: an all-failures run exited $rc, which would wedge the release"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}
