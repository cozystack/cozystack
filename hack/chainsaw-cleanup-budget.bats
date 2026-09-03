#!/usr/bin/env bats
# Asserts that the Chainsaw cleanup budget clears the floor the kubernetes
# chart's pre-delete hooks impose on it.
#
# The kubernetes e2e suites apply their Kubernetes CR declaratively, so the
# tenant cluster is torn down by Chainsaw's auto-cleanup rather than by an
# explicit delete op — which means `timeouts.cleanup` is what bounds a teardown
# that runs the chart's pre-delete hooks. Those hooks are sequential and
# blocking: packages/apps/kubernetes/templates/delete.yaml waits on the child
# HelmReleases and then on the TenantControlPlane, each with its own
# `--wait=true --timeout=`, and both are spent in full exactly when the tenant
# has no working nodes — the teardown case that file's own comments call
# expected. Their sum is a hard floor no cleanup can beat.
#
# A budget at or below that floor fails suites that have already passed every
# assertion they make. That is not hypothetical: on the run for
# cozystack/cozystack#4033 the budget was 300s against a 240s floor, and
# kubernetes-oidc-system passed every assertion and then failed CLEANUP at
# 303.72s, with Kamaji recording the TenantControlPlane gone 0.45s after
# Chainsaw gave up. Its structural twin kubernetes-oidc-customconfig finished
# the same teardown in 243.8s and passed, so the two bracketed one floor with
# 4s and 64s of variance above it against 60s of headroom.
#
# The margin below is what separates this from a guard that only restates the
# current number. Above the floor sit the `<release>-oidc-cleanup` hook Job
# (rendered whenever oidc.mode != None, bounded at 30s per attempt with
# backoffLimit 1 allowing a second, so 60s worst) and one cold start per hook
# Job (~30s each on a loaded runner) — 120s of variable cost that the floor
# itself does not account for. Requiring floor + 120s is therefore the
# inequality that has to hold for the budget to be reachable at all, and it is
# derived from both files rather than hardcoded, so adding a third blocking
# wait to the chart moves the floor and this test with it.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as
# a shell function under `set -eu -x`, so a non-zero exit is the failure.
# Paths are repo-root-relative: BATS_TEST_DIRNAME is unset and would abort the
# whole suite under `set -u`.
#
# Run with: hack/cozytest.sh hack/chainsaw-cleanup-budget.bats

# Minimum slack the budget must carry above the chart's blocking-wait floor,
# covering the oidc-cleanup hook Job's retry budget plus a cold start per Job.
CHAINSAW_CLEANUP_MIN_SLACK=120

# Convert a Chainsaw/Go duration that uses whole minutes or seconds into
# seconds. Deliberately narrow: the timeouts block only ever holds Nm or Ns,
# and a form this cannot read must fail loudly rather than silently score 0.
_duration_seconds() {
  _d=$1
  case "$_d" in
    *m) printf '%s\n' $(( ${_d%m} * 60 )) ;;
    *s) printf '%s\n' "${_d%s}" ;;
    *)
      echo "unrecognised duration '${_d}': expected Nm or Ns" >&2
      return 1
      ;;
  esac
}

@test "chainsaw cleanup budget clears the kubernetes pre-delete hook floor" {
  cfg=hack/e2e-chainsaw/.chainsaw.yaml
  hooks=packages/apps/kubernetes/templates/delete.yaml

  for f in "$cfg" "$hooks"; do
    if [ ! -f "$f" ]; then
      echo "missing $f; this guard reads both files and cannot be evaluated without them" >&2
      return 1
    fi
  done

  cleanup_raw=$(yq '.spec.timeouts.cleanup' "$cfg")
  if [ -z "$cleanup_raw" ] || [ "$cleanup_raw" = "null" ]; then
    echo "no .spec.timeouts.cleanup in $cfg; Chainsaw would fall back to its own default and this floor would be unguarded" >&2
    return 1
  fi
  cleanup_s=$(_duration_seconds "$cleanup_raw")

  # Sum the blocking pre-delete waits. A line counts only when it carries both
  # --wait=true (it blocks) and --timeout= (it is bounded); the prose comments
  # in that file mention --wait=true without either, and a commented-out flag
  # must not be scored as a live one.
  floor_s=0
  waits=0
  for t in $(grep -- '--wait=true' "$hooks" \
    | grep -v '^[[:space:]]*#' \
    | grep -oE -- '--timeout=[0-9]+s' \
    | grep -oE '[0-9]+'); do
    floor_s=$(( floor_s + t ))
    waits=$(( waits + 1 ))
  done

  if [ "$waits" -eq 0 ]; then
    echo "found no bounded blocking waits in $hooks; either the hooks stopped blocking (in which case this guard is obsolete) or the flags were reshaped and this extraction silently scored nothing" >&2
    return 1
  fi

  required=$(( floor_s + CHAINSAW_CLEANUP_MIN_SLACK ))
  echo "cleanup budget ${cleanup_raw} = ${cleanup_s}s; ${waits} blocking waits floor = ${floor_s}s; required >= ${required}s"

  if [ "$cleanup_s" -lt "$required" ]; then
    echo "timeouts.cleanup is ${cleanup_raw} (${cleanup_s}s) but the ${waits} blocking pre-delete waits in ${hooks} floor teardown at ${floor_s}s, and the oidc-cleanup hook Job plus per-Job cold starts add up to ${CHAINSAW_CLEANUP_MIN_SLACK}s more" >&2
    echo "a suite can then pass every assertion it makes and still fail CLEANUP, which is what cozystack/cozystack#4033 hit at 303.72s against a 300s budget" >&2
    echo "raise timeouts.cleanup to at least ${required}s, or shorten the blocking waits in the chart if teardown no longer needs them" >&2
    return 1
  fi
}
