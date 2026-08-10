#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Suite-wide invariant for hack/e2e-chainsaw: a test that applies a tenant
# `Kubernetes` CR ends only after that release's install action has returned.
#
# A Helm release cannot be uninstalled while its install action is still
# running, so a test that finishes its assertions mid-install leaves the rest of
# the install to be paid by Chainsaw's cleanup phase, on top of the teardown,
# against the single `cleanup` budget in hack/e2e-chainsaw/.chainsaw.yaml. The
# suite is then reported red on a cleanup step that tests nothing, seconds after
# every assertion passed. Both OIDC fixtures used to do exactly that: they
# deleted the CR three seconds after creating it, and across 19 in-tree E2E runs
# spent a median of 233s in cleanup against a 300s budget, crossing it in 3 of 38
# measured teardowns. Method, so the figure can be refuted rather than taken on
# trust: the 19 are every `Pull Request` workflow run in the window 31315492733
# to 31373204907 whose `E2E (in-tree)` job log was still retrievable, and each
# teardown is the wall-clock between that job's own `CLEANUP | BEGIN` and
# `CLEANUP | END`/`ERROR` lines for the two suites -- exact log timestamps, not
# event ages. The three that crossed are runs 31317302815, 31337303277 and
# 31373204907. 233s is the median of the 35 that finished; their maximum was 263s,
# and the three that did not finish are censored at the 300s ceiling rather than
# measured.
#
# Corroborated on a second, independent run: 31582632048, a different PR at head
# 5e470d507, where kubernetes-oidc-customconfig passed all four of its steps in
# 4.4556s with no failed assert and then took CLEANUP | ERROR with
# `context deadline exceeded`. From CLEANUP | BEGIN at 11:18:37.6723300 to
# CLEANUP | ERROR at 11:23:37.6720118 that is 299.9997s -- the 5m cleanup budget
# exhausted to within a third of a millisecond. Note which mark the clock starts
# on: measured from DELETE OK instead it reads 299.887s, and quoting that figure
# makes an exhausted budget look like a near miss.
#
# What the second run adds is provenance rather than sample size: it was reached
# from the symptom by someone who did not know this file was being written, so the
# mechanism is identifiable without the derivation below rather than fitted to it.
# What it does not add is an upper bound on the byo teardown -- that suite is
# censored at the budget in both runs, so two observations agree it exhausts 300s
# and neither shows what it would need.
#
# For the `Kubernetes` kind the install action outlives the render-side asserts
# by design. The kind carries release.cozystack.io/helm-install-disable-wait
# (packages/system/kubernetes-rd/cozyrds/kubernetes.yaml), so the install does
# not wait for the in-tenant addon HelmReleases -- but it does still wait for
# the chart's blocking post-install hook Jobs, and one of those polls for the
# Kamaji-issued admin kubeconfig. Asserting the HelmRelease Ready is what makes
# that wait explicit: it spends the same wall-clock against an assert with its
# own timeout and its own diff on failure, and leaves cleanup paying for the
# teardown alone.
#
# The rule is therefore two asserts on the LAST step: the release's HelmRelease
# at Ready, and then its `<release>-oidc-bootstrap` Job at `status.succeeded`.
# Both, because they are not interchangeable. The Job is the OIDC payload and
# survives its own success, so it can be named; the other blocking hook Job
# deletes itself on success and cannot be, which is what the HelmRelease assert
# covers. In that order, because the Ready wait is the one that has to carry a
# budget: a release cannot be Ready until every blocking hook Job has succeeded,
# so once it is, the Job assert reads a settled object instead of waiting for it,
# and one step does not get to spend two install-sized budgets in series.
#
# Checked by NAME and by FIELD rather than by shape, so that "the last step
# asserts something" cannot satisfy this. The expected names are derived, not
# hard-coded: `<release>` is the applied CR's own name behind the `release.prefix`
# the Kubernetes ApplicationDefinition declares, so a renamed fixture or a
# changed prefix moves the expectation with it instead of silently passing.
#
# On the LAST step, because a wait parked earlier lets the steps after it end
# the test mid-install again, and Chainsaw v0.2.15 has no test-level equivalent
# to hang it on. Note what this guard does NOT do: it says nothing about how
# long the teardown itself may take. Chainsaw's cleanup stays bounded and
# blocking, and a genuinely stuck uninstall still fails the suite -- see
# docs/agents/e2e-testing.md, "Do not mask cleanup or teardown failures".
#
# Discovery is generic: any future suite that applies a `Kubernetes` CR through
# `apply.file` is checked here without being named below. The walk runs in both
# directions, because a one-directional one goes quiet in exactly the case that
# matters: "did I find any fixture at all" stays true while one of two has
# dropped out of the match. So the manifests are enumerated from disk as well, and
# one that no Test claims is a discovery miss rather than a rule nobody is held
# to.
#
# What this checks about the budgets is only their relation to each other: the
# Job's must be shorter than the Ready one, and both must be readable. The Ready
# budget's own VALUE is argued in the fixture and held by review, not here -- it
# is derived there from the slowest legitimate install path and from what the
# whole failure path costs against the CI job cap, neither of which this guard can
# see. So `timeout: 6m` on a Ready assert passes here while contradicting that
# reasoning. Deliberate: pinning the value would mean reading the
# ApplicationDefinition's install timeout, the job cap and the catch budgets, and
# a guard assembled from three moving numbers reds on every legitimate change to
# any of them.
#
# `apply.file` is the limit of that reach, and there are TWO ways out of it, not
# one. The nearer one: a fixture that inlines the CR as `apply.resource:` leaves
# no manifest on disk, so neither direction of the walk can see it --
# `_tenant_cluster_manifests` skips `chainsaw-test.yaml` by name and the reverse
# case enumerates only sibling manifests, so nothing reds.
# `hack/e2e-chainsaw/gateway/chainsaw-test.yaml` already uses that form for other
# kinds. The farther one: a suite that creates its tenant cluster from a script --
# kubernetes-latest and kubernetes-previous do, from a heredoc in
# _lib/run-kubernetes.sh -- is invisible to this walk. Both of those wait far past
# install for worker nodes, so neither violates the rule in substance, but a
# future script-driven fixture would be green here while breaking it.
# docs/agents/e2e-testing.md carries the same qualifier so the checklist does not
# promise a machine check that does not exist.
#
# There is no exemption for a fixture whose subject is a FAILED install -- an
# OIDC validation-rejection case would be told to assert its HelmRelease Ready and
# could not. None exists today; if one is written, the exemption belongs here next
# to the mode gate below rather than as an annotation on the fixture.
#
# The Job rule applies only to a CR that asks for OIDC (`spec.oidc.mode` other
# than the schema's `None` default). The template renders that Job in every mode,
# but at `None` its work is a KamajiControlPlane normalisation unrelated to
# anything here, and a future non-OIDC tenant-cluster suite should not inherit an
# OIDC obligation from a guard named after cleanup.
#
# The mechanism this guard exists for -- a release whose install action is still
# running cannot be uninstalled, so ending a test there charges the remainder to
# `cleanup` -- is derived in docs/agents/e2e-testing.md, in this header, and in
# the kubernetes-oidc-system fixture where the budget follows from it. An
# explanation added anywhere else points at one of those rather than restating
# it: a restatement is a copy, nothing here compares the copies, and the one that
# drifts is the one nobody rereads.
#
# Maintenance tripwire: the synthetic-mutation case asserts that a nested run of
# this file reports exactly SIX @tests. Adding or removing an @test here reds that
# case until the constant beside `base_ran` is updated.
#
# Requires: yq (mikefarah v4+), jq, and python3. yq and jq are `make build-deps`
# requirements; python3 is NOT, so the case that needs it checks for it rather
# than assuming it. Unchecked, its absence is not a clean skip: the cases that
# need it fail with a message that blames the synthetic fixture, and the
# mode-gate case, whose expected outcome is GREEN, passes vacuously. python3 is
# needed because some of the breakages restructure YAML rather than substitute a
# line, which sed does not do safely; hack/md-no-hardwrap.bats sets the same
# precedent and guards it the same way.
#
# Run with: hack/cozytest.sh hack/chainsaw-tenant-cluster-cleanup.bats
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHAINSAW_DIR="${CHAINSAW_DIR:-$REPO_ROOT/hack/e2e-chainsaw}"
KUBERNETES_RD="${KUBERNETES_RD:-$REPO_ROOT/packages/system/kubernetes-rd/cozyrds/kubernetes.yaml}"
# Ceiling on the trailing Job assert, in seconds. Read where it is enforced for
# why an absolute number is needed alongside the comparison against the Ready
# budget.
_JOB_TIMEOUT_CAP=120

# A Chainsaw `timeout` in seconds, or -1 when the value cannot be read. Callers
# MUST test for -1 before comparing, rather than letting the comparison surface
# it: `-1 >= 1200` is false, so an unreadable Job budget passes the very check
# that exists to forbid an oversized one. The sentinel has to be rejected, not
# compared.
#
# Parses the Go/Chainsaw duration forms that can legitimately appear here
# (`20m`, `90s`, `1h`, `1m30s`) rather than the two suffixes this suite happens
# to use, because "a bit longer than a minute" is written `1m30s` and the
# suffix-only version accepted it as the integer 1m30 and then died inside
# `[ ]` with the test still reporting OK.
#
# Sub-second units are rejected rather than parsed (`500ms` -> -1). Go accepts
# them and nothing here would write one on an assert, so the safe direction is
# to refuse a form this cannot compare instead of guessing at it.
_to_seconds() {
  [ -n "$1" ] || { echo -1; return; }
  _rest=$1
  _total=0
  while [ -n "$_rest" ]; do
    _num=${_rest%%[hms]*}
    case "$_num" in ''|*[!0-9]*) echo -1; return ;; esac
    _unit=${_rest#"$_num"}
    _unit=${_unit%"${_unit#?}"}
    # Leading zeros stripped into a SEPARATE variable, by parameter expansion
    # rather than bash's `10#` base prefix. Two reasons, both found by running
    # this rather than reading it. `10#` is a runtime error under dash, which is
    # /bin/sh on the CI runner, so it broke every duration there while reading
    # green locally -- and it broke it the same way the octal trap above does, by
    # dying before the `echo` and handing the caller an empty string. And `$_num`
    # itself has to survive intact: the loop consumes `$_rest` by stripping
    # "$_num$_unit" off the front, so shortening `$_num` leaves `$_rest` unchanged
    # and the loop never terminates -- a hang, not a failure.
    _digits=$_num
    while :; do
      case "$_digits" in 0?*) _digits=${_digits#0} ;; *) break ;; esac
    done
    case "$_unit" in
      h) _total=$(( _total + _digits * 3600 )) ;;
      m) _total=$(( _total + _digits * 60 )) ;;
      s) _total=$(( _total + _digits )) ;;
      *) echo -1; return ;;
    esac
    _rest=${_rest#"$_num$_unit"}
  done
  echo "$_total"
}

# The release-name prefix cozystack-api puts in front of a Kubernetes CR's name.
_release_prefix() {
  yq '.spec.release.prefix' "$KUBERNETES_RD" 2>/dev/null
}

# Basenames of the manifests in suite dir $1 that carry a tenant Kubernetes CR.
_tenant_cluster_manifests() {
  for _m in "$1"/*.yaml; do
    [ -f "$_m" ] || continue
    [ "$(basename "$_m")" = "chainsaw-test.yaml" ] && continue
    if yq --output-format=json eval-all '.' "$_m" 2>/dev/null | jq -e -s '
         any(.[]; (.kind? == "Kubernetes")
                  and (((.apiVersion? // "") | startswith("apps.cozystack.io/"))))
       ' >/dev/null 2>&1; then
      basename "$_m"
    fi
  done
}

# The `apply.file` values of the Test document named $2 in the file $1.
_applied_files() {
  yq eval-all "
    select(.kind == \"Test\" and .metadata.name == \"$2\")
    | .spec.steps[].try[].apply.file // \"\"
  " "$1" 2>/dev/null
}

# The metadata.name of the tenant Kubernetes CR in the manifest $1. Refuses to
# answer for a manifest carrying more than one, rather than emitting two lines
# into the middle of a caller's single-line record.
_cr_name() {
  _names="$(yq eval-all '
    select(.kind == "Kubernetes"
           and ((.apiVersion // "") | test("^apps\.cozystack\.io/")))
    | .metadata.name
  ' "$1" 2>/dev/null)"
  if [ "$(printf '%s\n' "$_names" | grep -c .)" -gt 1 ]; then
    echo "GUARD-ERROR: $1 carries more than one tenant Kubernetes CR;" >&2
    echo "this guard's per-fixture record assumes one, so extend it before" >&2
    echo "adding a second: $(printf '%s' "$_names" | tr '\n' ' ')" >&2
    return 1
  fi
  printf '%s\n' "$_names"
}

# The spec.oidc.mode of the tenant Kubernetes CR in the manifest $1, defaulted
# the way the schema defaults it when the field is absent.
_cr_oidc_mode() {
  _mode="$(yq eval-all '
    select(.kind == "Kubernetes"
           and ((.apiVersion // "") | test("^apps\.cozystack\.io/")))
    | .spec.oidc.mode // "None"
  ' "$1" 2>/dev/null)"
  [ -n "$_mode" ] || _mode=None
  printf '%s\n' "$_mode"
}

# Lines "<chainsaw-test.yaml>|<Test name>|<release name>|<manifest basename>"
# for every Test that applies a tenant Kubernetes CR declaratively.
_tenant_cluster_tests() {
  _prefix="$(_release_prefix)"
  for _tf in "$CHAINSAW_DIR"/*/chainsaw-test.yaml; do
    [ -f "$_tf" ] || continue
    _dir="$(dirname "$_tf")"
    _mans="$(_tenant_cluster_manifests "$_dir")"
    [ -n "$_mans" ] || continue
    for _name in $(yq eval-all 'select(.kind == "Test") | .metadata.name' "$_tf" 2>/dev/null); do
      _applied="$(_applied_files "$_tf" "$_name")"
      for _m in $_mans; do
        if printf '%s\n' "$_applied" | grep -Fxq "$_m"; then
          # `continue`, not `return`: this loop body runs in a subshell because
          # of the `| sort -u` below, so a return status cannot leave it -- the
          # function's status is always sort's. Skipping is honest about that,
          # and it is not a hole: _cr_name has already printed GUARD-ERROR
          # naming the file, and the manifest it refused now belongs to no
          # record, which the reverse-discovery case reports as unclaimed.
          _cr="$(_cr_name "$_dir/$_m")" || continue
          echo "$_tf|$_name|${_prefix}${_cr}|$_m"
          # ONE manifest per Test, like _cr_name's one CR per manifest: the first
          # match wins and the rest are not recorded. Extend both before writing a
          # fixture that applies two tenant Kubernetes CRs from two files. It
          # fails closed rather than silently -- the second manifest ends up
          # claimed by no record, so the reverse-discovery case reds -- but it
          # reds with the wrong diagnosis, telling the author to check an
          # apply.file spelling that is correct.
          break
        fi
      done
    done
  done | sort -u
}

# Lines "<suite dir>|<manifest basename>" for every manifest carrying a tenant
# Kubernetes CR, whether or not a Test was found to apply it.
_tenant_cluster_manifests_all() {
  for _tf in "$CHAINSAW_DIR"/*/chainsaw-test.yaml; do
    [ -f "$_tf" ] || continue
    _dir="$(dirname "$_tf")"
    for _m in $(_tenant_cluster_manifests "$_dir"); do
      echo "$_dir|$_m"
    done
  done | sort -u
}

# "<apiVersion>/<kind> <name>" for each assert on the last step of the Test
# named $2 in file $1 that carries a Ready=True condition.
_last_step_ready_asserts() {
  yq eval-all "
    select(.kind == \"Test\" and .metadata.name == \"$2\")
    | .spec.steps[-1].try
    | to_entries
    | .[]
    | select(.value.assert.resource.status[\"(conditions[?type == 'Ready'])\"][0].status == \"True\")
    | (.key | tostring) + \" \" + .value.assert.resource.apiVersion + \"/\"
      + .value.assert.resource.kind + \" \" + .value.assert.resource.metadata.name
      + \" \" + (.value.assert.timeout // \"\")
  " "$1" 2>/dev/null
}

# "<apiVersion>/<kind> <name>" for each assert on the last step of the Test
# named $2 in file $1 that pins status.succeeded to a completion. The value is
# compared, not merely required to be present: `succeeded: 0` is a Job that has
# not completed, and a guard that accepted it would pass on the state it exists
# to reject -- the same way the Ready check here compares "True" rather than
# accepting any status.
_last_step_succeeded_asserts() {
  yq eval-all "
    select(.kind == \"Test\" and .metadata.name == \"$2\")
    | .spec.steps[-1].try
    | to_entries
    | .[]
    | select(.value.assert.resource.status.succeeded > 0)
    | (.key | tostring) + \" \" + .value.assert.resource.apiVersion + \"/\"
      + .value.assert.resource.kind + \" \" + .value.assert.resource.metadata.name
      + \" \" + (.value.assert.timeout // \"\")
  " "$1" 2>/dev/null
}

@test "_to_seconds reads the duration forms its contract names, and refuses the rest, and _count_verdicts holds its zero case" {
  # The parser is the single point whose failure silently disables the budget
  # check, and it has already done so once: the suffix-only version it replaced
  # took `1m30s` for the integer `1m30`, died inside `[ ]`, and left the test
  # reporting OK. Its header states a three-part contract -- which forms parse,
  # that unreadable input yields -1, and that callers must test for -1 before
  # comparing -- and the two fixtures only ever exercise `20m` and `1m`, so
  # nothing here held any of it. This walks the documented table instead.
  # 08m/090s are the leading-zero cases: shell arithmetic reads them as octal
  # and dies on the digit 8, which returned an empty string rather than -1 and
  # let an over-budget assert through as a result. 08m30s carries a leading zero
  # AND a second unit, which is the case that catches a fix stripping zeros from
  # the variable the outer loop consumes -- that one hangs rather than fails.
  for pair in 20m:1200 1m:60 1h:3600 1m30s:90 90s:90 1h30m:5400 08m:480 090s:90 0m:0 00m:0 08m30s:510; do
    want="${pair#*:}"
    got="$(_to_seconds "${pair%%:*}")"
    if [ "$got" != "$want" ]; then
      echo "_to_seconds ${pair%%:*} = $got, want $want" >&2
      exit 1
    fi
  done
  # Refused rather than guessed at: a bare number carries no unit, `20M` is not
  # the same token as `20m`, `500ms` is a form Go accepts that this cannot
  # compare, and an absent timeout must not read as zero.
  #
  # The list is refused by THREE different mechanisms, and knowing which is which
  # is what stops a future reader deleting an entry on the belief a neighbour
  # covers it. `''` and `*[!0-9]*` are two patterns of ONE `case` branch, so those
  # two are not separately removable:
  #   * 2d, 1x, abc, 60, 20M -- the unit test. No h/m/s, so the unit is empty.
  #   * 500ms -- the empty-numerator test, on the SECOND iteration: `500m` parses,
  #     `s` is left, and its numerator is empty. Not the unit test; the unit is
  #     valid there.
  #   * "" -- the `[ -n "$1" ]` guard at the top, before any branch runs.
  #   * a1m -- the digit test, and this one alone. Every other form survives its
  #     removal unchanged, so without `a1m` that branch is pinned by nothing.

  #
  # What removing the digit test actually does depends on the caller's shell
  # options, which is why it is worth stating rather than summarising. In bash
  # arithmetic a variable's VALUE is expanded as an expression, so the numeric
  # part `a1` becomes a lookup of a variable named `a1`. Under the `set -eu` this
  # file is run with, that is a loud `a1: unbound variable` and the function dies;
  # sourced anywhere without `-u`, it is a silent 0, and `0 >= 1200` is false, so
  # an unreadable Job timeout would PASS the budget check. Both outcomes are
  # wrong for a function whose contract is to answer -1, and only one of them is
  # visible -- so the digit test is what makes the answer independent of who
  # sourced it.
  for bad in 500ms 2d 1x abc 60 20M a1m ""; do
    got="$(_to_seconds "$bad")"
    if [ "$got" != "-1" ]; then
      echo "_to_seconds '$bad' = $got, want -1" >&2
      exit 1
    fi
  done
  # _count_verdicts, whose zero case is the one the baseline predicate depends on
  # and the only one that cannot be reached through a healthy nested run. Both
  # halves are load-bearing. The zero case fails if the `|| true` is dropped, as
  # the assignment then carries grep's non-zero status and `set -e` ends the test
  # here. It fails differently, on the value, if the guard is written as
  # `|| echo 0`, because grep has already printed a 0 and the second one arrives
  # on its own line. The counting case is what stops either being "fixed" by
  # swallowing the count itself.
  #
  # Skipped in the nested run, and not for symmetry with the synthetic case: this
  # block cannot execute there without corrupting the measurement that reads that
  # run. cozytest traces every command, so `_count_verdicts`'s own
  # `grep -cE '(✅ Test OK|❌ Test failed)'` line -- and the fixture lines below --
  # reach the nested run's captured output, where the counter reads them as
  # verdicts. Measured: the count went from 6 to 9 and `_guard_over` reported a
  # failing case named after a fixture string. The markers cannot be written in a
  # test that the counter is watching.
  if [ -n "${GUARD_SYNTH_RUN:-}" ]; then return 0; fi
  # Both checks are on the VALUE, and deliberately not on the status, because a
  # status assertion here would be vacuous: cozytest rewrites every line that is
  # exactly `}` into `return 0` before the brace, which appends a `return 0` to
  # each function in this file, so no function here can report failure through its
  # exit status. Measured in both shells, against the harness's generated file.
  #
  # What that leaves, also measured. Writing the guard as `|| echo 0` is caught in
  # dash and bash alike, on the value: grep has already printed its 0, so the echo
  # adds a second line. Dropping the guard altogether is caught under dash only,
  # where errexit still reaches into a command substitution and ends the test at
  # the assignment; under bash the assignment survives and 0 is what a correct
  # counter returns too. CI's `/bin/sh` is dash and a macOS `/bin/sh` is bash, so
  # that half holds where the suite runs in anger and not on a laptop. Do not
  # "strengthen" this into a status check -- the harness cannot carry one.
  got="$(printf 'a line\nanother line\n' | _count_verdicts)"
  if [ "$got" != "0" ]; then
    echo "_count_verdicts with no verdict lines = '$got', want 0" >&2
    exit 1
  fi
  got="$(printf '✅ Test OK: a\nnoise\n❌ Test failed: b (exit 1)\n' | _count_verdicts)"
  if [ "$got" != "2" ]; then
    echo "_count_verdicts with two verdict lines = '$got', want 2" >&2
    exit 1
  fi
}

# Build a throwaway suite dir under $1 whose Test is compliant, then let the
# caller break one thing. Writing the fixture rather than mutating the real one
# is what lets these cases run without a cluster and without touching the tree,
# the same shape hack/remediation-guard.bats uses for its own synthetic input.
_synth_suite() {
  _d="$1/synth"
  mkdir -p "$_d"
  cat >"$_d/synth-app.yaml" <<'YAML'
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: synth
spec:
  version: v1.35
  oidc:
    mode: System
YAML
  # Release name derived, not spelled out: the guard builds its expectation from
  # the ApplicationDefinition's release.prefix, so a hard-coded one here turns a
  # prefix change into "the fixture below is wrong, not the guard" -- loud, but
  # pointing at the wrong file. Heredoc unquoted for that one substitution.
  _synth_release="$(_release_prefix)synth"
  cat >"$_d/chainsaw-test.yaml" <<YAML
apiVersion: chainsaw.kyverno.io/v1alpha1
kind: Test
metadata:
  name: synth
spec:
  steps:
  - name: create
    try:
    - apply:
        file: synth-app.yaml
  - name: wait
    try:
    - assert:
        timeout: 20m
        resource:
          apiVersion: helm.toolkit.fluxcd.io/v2
          kind: HelmRelease
          metadata:
            name: ${_synth_release}
          status:
            (conditions[?type == 'Ready']):
            - status: "True"
    - assert:
        timeout: 1m
        resource:
          apiVersion: batch/v1
          kind: Job
          metadata:
            name: ${_synth_release}-oidc-bootstrap
          status:
            succeeded: 1
YAML
  echo "$_d"
}

# Counts verdict lines on stdin. Separate from the runner below so that the zero
# case is reachable from a test, and because that case is the one that has to
# survive: `grep -c` prints 0 and exits 1 when nothing matches, the caller
# assigns this through a command substitution, and a simple assignment carries
# the substitution's status -- so without `|| true` the caller aborts under
# `set -e` at the assignment, taking with it the "none ran" diagnostic this count
# exists to print. `|| true` rather than `|| echo 0`: grep has already printed
# its 0, so an echo appends a second line and the numeric comparison downstream
# then fails on a two-line value instead.
_count_verdicts() {
  grep -cE '(✅ Test OK|❌ Test failed)' || true
}

# How many @tests the nested run actually executed. Used to make the baseline
# predicate observable: "green" alone cannot distinguish "all six passed" from
# "none ran".
_guard_test_count() {
  ( GUARD_SYNTH_RUN=1 CHAINSAW_DIR="$1" "$REPO_ROOT/hack/cozytest.sh" \
      "$REPO_ROOT/hack/chainsaw-tenant-cluster-cleanup.bats" 2>&1 || true ) \
    | _count_verdicts
}

# Run the whole guard against a synthetic CHAINSAW_DIR and echo the failing case
# names, or "GREEN" if it passed. `|| true`: a red is the expected outcome here.
_guard_over() {
  # A default value and not a sed substitution on an empty line: with no failures
  # the pipeline emits no LINE at all, so `sed 's/^$/GREEN/'` never runs and the
  # caller receives an empty string, which is indistinguishable here from a
  # baseline that is not green.
  # Through a file, not a variable: cozytest runs each @test under `set -x`, so
  # assigning the nested run's entire output traces that output into the parent's
  # log -- one nested run per case, each reprinting every verdict. The extracted
  # names and findings are short enough to trace harmlessly.
  _raw_f="$(mktemp)"
  ( GUARD_SYNTH_RUN=1 CHAINSAW_DIR="$1" "$REPO_ROOT/hack/cozytest.sh" \
      "$REPO_ROOT/hack/chainsaw-tenant-cluster-cleanup.bats" >"$_raw_f" 2>&1 || true )
  # GREEN is decided by "no @test failed", never by "no finding was printed".
  # Those differ: a failure outside the rule tests -- discovery, the parser --
  # emits no `(<fixture>): <finding>` line at all, so a run keyed on findings
  # alone would call it GREEN. Case 9 asserts GREEN, so that would have turned an
  # unrelated breakage into a passing case.
  _names="$(sed -n 's/.*Test failed: \(.*\) (exit.*/\1/p' "$_raw_f" | sort -u | paste -sd, -)"
  if [ -z "$_names" ]; then rm -f "$_raw_f"; echo "GREEN"; return; fi
  # Findings, not @test names, because one @test holds several branches and they
  # are what the cases below distinguish. Keyed on the name alone, deleting a
  # presence branch would leave its case green: the same input falls through to the
  # readability branch, which reds the same @test under a different finding.
  _msgs="$(sed -n 's/.*chainsaw-test\.yaml): //p' "$_raw_f" | sed "s/'\$//" | sort -u | paste -sd'|' -)"
  rm -f "$_raw_f"
  echo "$_names :: $_msgs"
}

@test "each rule bites on a synthetic fixture that breaks exactly one thing" {
  # This case runs the whole file against a synthetic tree, so it must exclude
  # itself: without the guard below, each level spawns another full run.
  if [ -n "${GUARD_SYNTH_RUN:-}" ]; then return 0; fi
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  # Checked, not assumed: python3 is not a build-dep, and without it the cases
  # that restructure YAML fail with a message that blames the fixture while
  # the mode-gate case below passes vacuously, because its expected outcome is
  # green. A missing interpreter must not be able to look like a satisfied rule.
  command -v python3 >/dev/null || { echo "python3 is required by this case" >&2; exit 1; }
  # A rule exercised only against a tree that satisfies it passes identically
  # with the rule deleted, so each case below breaks exactly one thing and names
  # the FINDING that must appear -- not the @test, which carries several branches.
  tmp="$(mktemp -d)"
  d="$(_synth_suite "$tmp")"
  # Two conditions, not one, and the second is narrower than it first looks.
  # "Baseline is GREEN" is unobservable if removed: the baseline IS green, so
  # deleting the check changes nothing on real data. Requiring the expected number
  # of REPORTED tests adds a condition that a wrong number does violate, and a
  # mutation setting it to 7 dies here.
  #
  # What it does NOT prove: that each of those six tests did any work. A test that
  # returns early still prints its verdict line and is still counted -- the
  # synthetic case below does exactly that under GUARD_SYNTH_RUN, by design. An
  # early return added to any other @test passes this check too, and comparing
  # reported NAMES instead of the count does not close that, for the same reason:
  # an early return reports its name.
  #
  # So the scope is "six tests reported", not "the file was exercised".
  # Distinguishing the two needs each nested test to emit evidence of its own
  # work, which is a larger change than this case justifies.
  base="$(_guard_over "$tmp")"
  base_ran="$(_guard_test_count "$tmp")"
  # GREEN is tested FIRST, and the order carries a diagnosis rather than a
  # verdict. cozytest.sh exits at its first failing test, so a genuinely broken
  # synthetic baseline produces fewer than six verdict lines as a SIDE EFFECT of
  # failing -- and checking the count first then blames the set of @tests being
  # executed, which is the wrong reason for the likelier fault. Both orders red;
  # only this one names the cause.
  if [ "$base" != "GREEN" ]; then
    echo "synthetic baseline is not green ($base); the fixture below is wrong, not the guard" >&2
    echo "synthetic tree left at $tmp for inspection" >&2
    exit 1
  fi
  if [ "$base_ran" -ne 6 ]; then
    echo "synthetic baseline reported $base_ran tests, expected 6 -- the nested run is not executing the set of @tests this case reasons about" >&2
    echo "synthetic tree left at $tmp for inspection" >&2
    exit 1
  fi

  # _expect <must-appear> <description> [<must-NOT-appear>]
  #
  # The third argument pins the `continue` in a presence branch, which nothing
  # else does. Drop it and the rule still bites with the right primary finding,
  # so a check on the primary alone passes -- while the walk carries on into the
  # branches below with an empty value and reports a second finding about a
  # timeout on an object that is not there. That is the same wrong-diagnosis
  # defect as keying on @test names, reached by removing a different line of the
  # same block. A block is a condition, a body and a transfer of control, and
  # each has to be removable on its own for the set to be complete.
  _expect() {
    got="$(_guard_over "$tmp")"
    case "$got" in
      *"$1"*) : ;;
      *) echo "breaking '$2' gave [$got], expected a failure matching '$1'" >&2
         echo "synthetic tree left at $tmp for inspection" >&2
         exit 1 ;;
    esac
    [ -n "${3:-}" ] || return 0
    case "$got" in
      *"$3"*) echo "breaking '$2' also reported '$3' -- a finding about a field on an object that is absent; the branch that names the absence must stop the walk" >&2
              echo "synthetic tree left at $tmp for inspection" >&2
              exit 1 ;;
    esac
  }
  orig="$(cat "$d/chainsaw-test.yaml")"

  # 1. no Ready assert at all
  printf '%s' "$orig" | grep -v "helm.toolkit.fluxcd.io/v2" > "$d/chainsaw-test.yaml"
  _expect "last step does not assert HelmRelease" "Ready assert removed" "carries no positive timeout"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 2. Ready assert with no readable timeout
  printf '%s' "$orig" | sed '/^        timeout: 20m$/d' > "$d/chainsaw-test.yaml"
  _expect "carries no positive timeout" "Ready timeout removed"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 3. Job budget not shorter than Ready, at a scale where ONLY that comparison
  #    can see it. Both budgets are put under the absolute cap, so the rule below
  #    stays silent and this case pins the comparison alone. Written the obvious
  #    way, with both budgets at 20m, it would pin nothing: the cap catches 20m
  #    too, so the case stays red even with the comparison neutered. Every value
  #    the real fixtures use sits on one side of the cap, which is why the
  #    discriminating state has to be built here.
  printf '%s' "$orig" | sed 's/^        timeout: 20m$/        timeout: 90s/; s/^        timeout: 1m$/        timeout: 90s/' > "$d/chainsaw-test.yaml"
  _expect "is not shorter than" "Job budget equal to Ready, both under the cap"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 4. Job budget under Ready but still install-sized. Distinct from case 3: that
  #    one trips the comparison against Ready, this one is shorter than Ready and
  #    only the absolute cap can see it. Ready at 20m leaves room for a 15m Job
  #    budget, which is the whole reason the cap exists.
  printf '%s' "$orig" | sed 's/^        timeout: 1m$/        timeout: 15m/' > "$d/chainsaw-test.yaml"
  _expect "exceeds the" "Job budget install-sized but under Ready"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 5. asserts in the wrong order. Swapping the two wholesale is easier to do by
  #    rewriting the step than by patching lines.
  python3 - "$d/chainsaw-test.yaml" <<'PY2'
import sys
p = sys.argv[1]
s = open(p).read()
hr = s[s.index("    - assert:\n        timeout: 20m"):s.index("    - assert:\n        timeout: 1m")]
job = s[s.index("    - assert:\n        timeout: 1m"):]
open(p, "w").write(s[:s.index(hr)] + job.rstrip("\n") + "\n" + hr)
PY2
  _expect "precedes the HelmRelease Ready assert" "asserts swapped"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 6. no Job assert at all, at a mode that requires one. This is the rule the
  #    @test below is named after, and this case is the only thing pinning it:
  #    removing the branch that records the finding leaves every other case green,
  #    because case 9 deletes the same assert at mode None where green is the
  #    right answer, so it pins the mode gate instead. The two differ only by the
  #    mode, which is why one can look like it covers the other.
  python3 - "$d/chainsaw-test.yaml" <<'PY2'
import sys
p = sys.argv[1]
s = open(p).read()
open(p, "w").write(s[:s.index("    - assert:\n        timeout: 1m")])
PY2
  _expect "last step does not assert Job/" "Job assert deleted at mode System" "cannot read a positive timeout"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 6b. a zero budget on the Ready wait. Parses cleanly, so the readability test
  #     alone accepted it; it is a wait that expires before it starts.
  printf '%s\n' "$orig" | sed 's/^        timeout: 20m$/        timeout: 0m/' > "$d/chainsaw-test.yaml"
  _expect "carries no positive timeout" "Ready budget of zero"
  printf '%s\n' "$orig" > "$d/chainsaw-test.yaml"

  # 6c. a zero budget on the Job assert. Same boundary as 6b, other side of the
  #     comparison -- with -lt, 0 passes both this check and the two below it.
  printf '%s\n' "$orig" | sed 's/^        timeout: 1m$/        timeout: 0m/' > "$d/chainsaw-test.yaml"
  _expect "cannot read a positive timeout" "Job budget of zero"
  printf '%s\n' "$orig" > "$d/chainsaw-test.yaml"

  # 6d. a SECOND Ready assert on the same release, the bad one first. This is the
  #     state none of the other cases reach, and the duplicate check is what makes
  #     it visible: the order rule reads the first assert's index while the budget
  #     rule reads the last assert's timeout, so a `0m` wait -- the one chainsaw
  #     actually runs, expiring instantly -- otherwise hides behind a healthy 20m
  #     duplicate.
  python3 - "$d/chainsaw-test.yaml" <<'PY2'
import sys
p = sys.argv[1]
s = open(p).read()
hr = s[s.index("    - assert:\n        timeout: 20m"):s.index("    - assert:\n        timeout: 1m")]
open(p, "w").write(s.replace(hr, hr.replace("timeout: 20m", "timeout: 0m", 1) + hr, 1))
PY2
  _expect "Ready more than once" "duplicate Ready assert, zero budget first"
  printf '%s\n' "$orig" > "$d/chainsaw-test.yaml"

  # 6e. the same hazard on the Job assert, and the same asymmetry: an
  #     install-sized budget first, a healthy 1m second. The cap rule reads the
  #     last line's timeout and passes; the effective assert is the oversized one.
  python3 - "$d/chainsaw-test.yaml" <<'PY2'
import sys
p = sys.argv[1]
s = open(p).read()
job = s[s.index("    - assert:\n        timeout: 1m"):]
open(p, "w").write(s.replace(job, job.replace("timeout: 1m", "timeout: 15m", 1) + job, 1))
PY2
  _expect "more than once" "duplicate Job assert, install-sized budget first"
  printf '%s\n' "$orig" > "$d/chainsaw-test.yaml"

  # 7. Job budget in a form the parser cannot read. Only the readability branch
  #    can catch this: an unreadable duration reads back as -1, and both numeric
  #    comparisons are false against it (-1 is neither >= the Ready budget nor
  #    over the cap), which is why that branch has to reject before comparing
  #    rather than trusting the comparison to notice.
  printf '%s' "$orig" | sed 's/^        timeout: 1m$/        timeout: 500ms/' > "$d/chainsaw-test.yaml"
  _expect "cannot read a positive timeout" "Job budget unreadable"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 8. one second over the cap. Case 4 is far past it and would still fail if the
  #    constant were wrong by minutes; this pins the boundary itself.
  printf '%s' "$orig" | sed 's/^        timeout: 1m$/        timeout: 121s/' > "$d/chainsaw-test.yaml"
  _expect "exceeds the" "Job budget one second over the cap"
  printf '%s' "$orig" > "$d/chainsaw-test.yaml"

  # 10. the manifest-to-Test direction, which every case above leaves untested:
  #     they all break an assert inside a Test the forward walk has already
  #     matched, so the reverse rule only ever runs against a tree that satisfies
  #     it. Measured, not inferred -- a `return 0` at the top of
  #     _tenant_cluster_manifests_all leaves all six @tests green and the run at
  #     exit 0, which is the shape this case exists to kill.
  #
  #     A second manifest rather than a misspelled `apply.file`, so that exactly
  #     one thing breaks: respelling the Test's `file:` also empties the forward
  #     walk, which reds the discovery case too and makes the case red for two
  #     reasons. A copy no Test names leaves the forward walk intact.
  #
  #     Keyed on the @test name, which the cases above avoid on purpose because
  #     one @test carries several branches. That reason does not apply here: this
  #     is that @test's only failure branch, so the name identifies it uniquely.
  cp "$d/synth-app.yaml" "$d/stray-app.yaml"
  _expect "reached by discovery" "a manifest no Test applies"
  rm -f "$d/stray-app.yaml"

  # 9. the mode gate: at mode None the Job rule lifts and the Ready rule does not
  sed -i.bak 's/    mode: System/    mode: None/' "$d/synth-app.yaml" && rm -f "$d/synth-app.yaml.bak"
  python3 - "$d/chainsaw-test.yaml" <<'PY2'
import sys
p = sys.argv[1]
s = open(p).read()
open(p, "w").write(s[:s.index("    - assert:\n        timeout: 1m")])
PY2
  got="$(_guard_over "$tmp")"
  if [ "$got" != "GREEN" ]; then
    echo "at mode None with no Job assert the guard should pass, got [$got]" >&2
    rm -rf "$tmp"; exit 1
  fi
  rm -rf "$tmp"
}

@test "discovery finds the fixtures that apply a tenant Kubernetes CR" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  # Not a claim about how many such fixtures exist -- only that the walk above
  # still resolves manifests and Test documents. Zero means the discovery broke
  # (renamed directory, changed apply shape, yq behaviour change), and the
  # assertion below would then pass by finding nothing to check.
  found="$(_tenant_cluster_tests)"
  if [ -z "$found" ]; then
    echo "discovery found no Chainsaw Test applying an apps.cozystack.io Kubernetes CR;" >&2
    echo "either the suite no longer has one, or this guard's walk is broken" >&2
    exit 1
  fi
  echo "discovered:" >&2
  printf '%s\n' "$found" >&2
}

@test "every manifest carrying a tenant Kubernetes CR is reached by discovery" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  # The case above only asks whether the walk found ANYTHING, which two fixtures
  # satisfy while one of them has silently dropped out. This one closes that
  # from the other side: the manifests are enumerated from disk, so a fixture
  # whose `apply.file` no longer matches its manifest basename -- `./name.yaml`,
  # or a glob, both of which Chainsaw accepts -- shows up as a manifest nothing
  # claims, instead of as an obligation nobody is held to.
  matched="$(_tenant_cluster_tests | awk -F'|' '{print $1"|"$4}' \
             | sed 's#/chainsaw-test.yaml|#|#' | sort -u)"
  missed=""
  for row in $(_tenant_cluster_manifests_all); do
    if ! printf '%s\n' "$matched" | grep -Fxq "$row"; then
      echo "no Chainsaw Test applies ${row#*|} in ${row%%|*}" >&2
      missed="${missed}${missed:+ }$row"
    fi
  done
  if [ -n "$missed" ]; then
    echo "these tenant Kubernetes CR manifests are not reached by discovery, so" >&2
    echo "the rules below are not applied to whatever applies them: $missed" >&2
    echo "Check the apply.file spelling against the manifest basename." >&2
    exit 1
  fi
}

@test "a fixture applying a tenant Kubernetes CR waits for its HelmRelease on the last step" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  bad=""
  for entry in $(_tenant_cluster_tests); do
    tf="${entry%%|*}"
    rest="${entry#*|}"
    name="${rest%%|*}"
    release="${rest#*|}"
    release="${release%%|*}"
    # `steps[-1]` and not "some step": a Ready wait parked on an earlier step
    # leaves the steps after it free to end the test while the install is still
    # running, which is the state this guard exists to prevent. The release name
    # is matched too, so asserting some OTHER release's HelmRelease Ready --
    # which says nothing about this test's install -- does not satisfy it.
    ready="$(_last_step_ready_asserts "$tf" "$name" \
             | grep -F " helm.toolkit.fluxcd.io/v2/HelmRelease $release " || true)"
    if [ -z "$ready" ]; then
      echo "$name ($tf): last step does not assert HelmRelease/$release Ready" >&2
      bad="${bad}${bad:+ }$name"
      continue
    fi
    # Refuse on more than one match rather than reading it, the way _cr_name
    # refuses on more than one CR. The two consumers below read DIFFERENT lines
    # out of a multi-line match -- `${var%% *}` takes the first line's index and
    # `${var##* }` the last line's timeout -- so a second Ready assert on the same
    # release makes the order check describe one assert and the budget check
    # another. A `timeout: 0m` assert hidden ahead of a good one then passes every
    # rule while being the assert chainsaw actually runs, which is the false red
    # this whole change exists to remove.
    if [ "$(printf '%s\n' "$ready" | grep -c .)" -gt 1 ]; then
      echo "$name ($tf): last step asserts HelmRelease/$release Ready more than once; this guard reads one and cannot say which one chainsaw waits on" >&2
      bad="${bad}${bad:+ }$name"
      continue
    fi
    # Readability of the Ready budget is checked HERE and not only beside the
    # budget comparison, because that comparison sits in the Job case, which is
    # gated on the CR asking for OIDC. A future non-OIDC tenant-cluster fixture
    # with `assert:` and no `timeout:` would pass both cases and then inherit
    # timeouts.assert (5m in ../.chainsaw.yaml) -- a quarter of the install it is
    # supposed to outlast, which is the false red this whole rule removes.
    # -le, not -lt: `0m` parses cleanly to 0, so a readability test alone let it
    # through. A zero budget is not an unreadable one, it is a wait that expires
    # before it starts -- the failure this step exists to prevent, spelled as a
    # legal duration.
    if [ "$(_to_seconds "${ready##* }")" -le 0 ]; then
      echo "$name ($tf): the HelmRelease Ready assert carries no positive timeout this guard can read ('${ready##* }'); an absent one inherits timeouts.assert and is far shorter than the install, and a zero one expires immediately" >&2
      bad="${bad}${bad:+ }$name"
    fi
  done
  if [ -n "$bad" ]; then
    echo "these fixtures can end while the release install is still running," >&2
    echo "leaving the rest of the install to Chainsaw's cleanup budget: $bad" >&2
    echo "Add an assert on the HelmRelease Ready condition as the last step." >&2
    exit 1
  fi
}

@test "a fixture applying a tenant Kubernetes CR waits for its OIDC bootstrap Job on the last step" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
  bad=""
  for entry in $(_tenant_cluster_tests); do
    tf="${entry%%|*}"
    rest="${entry#*|}"
    name="${rest%%|*}"
    rest="${rest#*|}"
    release="${rest%%|*}"
    manifest="${rest#*|}"
    # Only for a CR that actually asks for OIDC. The Job is rendered by
    # oidc-rbac-job.yaml for every mode including None, but at mode None it does
    # a KamajiControlPlane normalisation that has nothing to do with the rule
    # this file is about -- so requiring it there would make a future non-OIDC
    # tenant-cluster suite inherit an OIDC-shaped obligation from a guard named
    # after cleanup. The mode is read from the applied CR, defaulted to None the
    # way the schema defaults it.
    mode="$(_cr_oidc_mode "$(dirname "$tf")/$manifest")"
    [ "$mode" = "None" ] && continue
    # Separate from the HelmRelease case above, and not folded into it, because
    # the two cover different things: this one is the OIDC payload -- the Job
    # that writes the per-user ClusterRoleBindings inside the tenant -- and it
    # is the only blocking post-install hook Job that outlives its own success,
    # so it is the only one that can be asserted by name at all.
    job="$(_last_step_succeeded_asserts "$tf" "$name" \
           | grep -F " batch/v1/Job ${release}-oidc-bootstrap " || true)"
    if [ "$(printf '%s\n' "$job" | grep -c .)" -gt 1 ]; then
      echo "$name ($tf): last step asserts Job/${release}-oidc-bootstrap more than once; same reading hazard as the Ready assert above" >&2
      bad="${bad}${bad:+ }$name"
      continue
    fi
    if [ -z "$job" ]; then
      echo "$name ($tf): last step does not assert Job/${release}-oidc-bootstrap at status.succeeded" >&2
      bad="${bad}${bad:+ }$name"
      # Stop here, as the Ready presence branch above does. Falling through ran
      # the order and budget checks against an empty value, which reported a
      # second finding -- "cannot read a timeout ... Job=''" -- about a field on
      # an assert that is absent. Nothing useful comes from continuing: the
      # index comparison against an empty string returns status 2, which `if`
      # reads as false, so the order check silently answers "fine" as well.
      continue
    fi
    # Order and budget, not just presence. Why order is the load-bearing half and
    # budget the lesser one is derived in this file's header -- kept as a pointer
    # rather than a fourth copy, for the same reason the fixtures point at each
    # other instead of repeating the podLogs rationale: a correction otherwise has
    # to be made in every copy, and the last one is the one that gets missed.
    ready="$(_last_step_ready_asserts "$tf" "$name" \
             | grep -F " helm.toolkit.fluxcd.io/v2/HelmRelease $release " || true)"
    # Derived here and not inherited: cozytest.sh turns each @test into its own
    # shell function, so a variable computed in the case above is not in scope
    # down here. An absent Ready assert is that case's finding, not this one's.
    [ -n "$ready" ] || continue
    ready_idx="${ready%% *}"
    job_idx="${job%% *}"
    if [ "$ready_idx" -ge "$job_idx" ]; then
      echo "$name ($tf): the Job assert (index $job_idx) precedes the HelmRelease Ready assert (index $ready_idx); the Ready wait has to come first" >&2
      bad="${bad}${bad:+ }$name"
    fi
    ready_to="${ready##* }"
    job_to="${job##* }"
    ready_secs="$(_to_seconds "$ready_to")"
    job_secs="$(_to_seconds "$job_to")"
    if [ "$ready_secs" -le 0 ] || [ "$job_secs" -le 0 ]; then
      # Rejected before comparing, not folded into the comparison: an
      # unreadable duration is the one input for which the numeric test cannot
      # answer, and letting it through is how an over-budget value passes.
      echo "$name ($tf): cannot read a positive timeout on the last step (Ready='$ready_to', Job='$job_to'); both must be a duration this guard can compare, and neither may be zero" >&2
      bad="${bad}${bad:+ }$name"
    elif [ "$job_secs" -ge "$ready_secs" ]; then
      echo "$name ($tf): the Job assert timeout ($job_to) is not shorter than the Ready timeout ($ready_to); it reads a settled object and must not carry an install-sized budget" >&2
      bad="${bad}${bad:+ }$name"
    elif [ "$job_secs" -gt "$_JOB_TIMEOUT_CAP" ]; then
      # Absolute, because the relative test above cannot express this one. A
      # Ready budget sized for an install makes room underneath it for a Job
      # budget that is itself install-sized -- 15m under 20m is "shorter" and
      # is the reversal the comment above says must not be made to look
      # affordable. The relative test caught that while the Ready budget was
      # 10m and stopped catching it when the Ready budget grew, which is the
      # wrong way round: raising the wait must not widen what may hide beneath
      # it. The cap is a policy value and not derived. Both fixtures ask 1m, so
      # this leaves one doubling for a slower poll, and it is far under the
      # smallest HELMRELEASE Ready budget in the tree (5m: external-dns,
      # foundationdb, kuberture, openbao, qdrant and the byo fixture), which is
      # the scale it has to stay away from. Note what it is NOT under: the
      # smallest Ready-condition assert anywhere in hack/e2e-chainsaw is 2m, on a
      # TenantGateway in the gateway suite, and 120s equals it. That is fine
      # because a TenantGateway is not a Helm install, but the qualifier belongs
      # in the sentence -- without "HelmRelease" the claim is false, and false in
      # the direction that invents headroom.
      echo "$name ($tf): the Job assert timeout ($job_to) exceeds the ${_JOB_TIMEOUT_CAP}s cap for this assert; it reads an object the Ready assert ahead of it has already settled, so it polls rather than waits" >&2
      bad="${bad}${bad:+ }$name"
    fi
  done
  if [ -n "$bad" ]; then
    echo "the OIDC bootstrap Job is unchecked in: $bad" >&2
    echo "Add an assert on Job/<release>-oidc-bootstrap status.succeeded as the last step." >&2
    exit 1
  fi
}
