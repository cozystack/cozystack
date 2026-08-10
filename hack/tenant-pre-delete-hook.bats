#!/usr/bin/env bats
# Behavioural tests for the pre-delete cleanup hook of packages/apps/tenant: the
# script is extracted from the rendered Job and executed against a stub kubectl.
# The helm-unittest cases in packages/apps/tenant/tests/cleanup_role_watch_test.yaml
# assert the Job's shape — the deadline and the attempt count — which is what
# the script cannot observe about itself.
#
# What a stub can and cannot show. `--timeout` is implemented inside kubectl, so
# no stub can make that deadline actually elapse; what the stub can do is record
# whether the flag reached kubectl at all, and answer for the run that follows a
# delete which gave up. Those are the two halves below: one test reads the
# arguments, the rest inject the non-zero exit an expired delete produces.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. The runner has no setup/teardown, so each
# @test builds its own fixture and removes it at the end of the body. No EXIT
# traps: a test that dies inside one prints neither `not ok` nor a reason, and
# the suite reads green. A failing test therefore leaves its temp directory
# behind, which is what you want when reading the rendered script afterwards.

CHART=packages/apps/tenant

# The two waves, told apart by their label selector. `*tenantmodule=true*` does
# not match the applications wave: that one spells the selector
# `tenantmodule!=true`, which contains no `tenantmodule=true` substring.
WAVE_APPS='*tenantmodule!=true*'
WAVE_MODULES='*tenantmodule=true*'

# Render the pre-delete Job into $1. The release name has to start with
# `tenant-` and carry no second dash (templates/tenant.yaml enforces it), and
# `_cluster` normally arrives from the cozystack-values Secret, so the one key
# templates/keycloakgroups.yaml indexes is supplied here as a string.
#
# The emptiness check must be an early `return 1`: hack/cozytest.sh rewrites
# every line matching ^}$ into `return 0` plus `}`, helpers included, so a check
# left in last position has its status discarded. helm's stderr is kept rather
# than dropped, because it carries the reason a render failed; only the
# per-render notice about the charts/cozy-lib symlink is filtered out.
render_job() {
  helm template tenant-test "$CHART" \
    --namespace tenant-root \
    --set-string '_cluster.oidc-enabled=false' \
    --show-only templates/cleanup-job.yaml 2>"$1.err" |
    yq 'select(.kind == "Job")' - > "$1"
  if [ ! -s "$1" ]; then
    echo "FAIL: the chart rendered no pre-delete Job" >&2
    grep -v 'found symbolic link in path' "$1.err" >&2 || true
    return 1
  fi
}

# A stub kubectl in $1. Every invocation is logged; those matching a glob in
# $KUBECTL_FAIL exit non-zero with the wording kubectl itself prints when a
# `--wait` deadline expires, which is the only outcome the script can tell apart
# from success.
#
# The closing brace of the helper inside the heredoc is indented on purpose:
# cozytest.sh rewrites every line matching ^}$ anywhere in this file, the
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
  echo "error: timed out waiting for the condition on helmreleases.helm.toolkit.fluxcd.io" >&2
  exit 1
fi
exit 0
STUB
  chmod 0755 "$1/kubectl"
}

# Fixture: $tmp holds the rendered Job, its script, and the stub kubectl.
setup_case() {
  tmp=$(mktemp -d) || return 1
  render_job "$tmp/job.yaml" || return 1
  yq '.spec.template.spec.containers[0].command[2]' "$tmp/job.yaml" > "$tmp/s.sh" || return 1
  [ -s "$tmp/s.sh" ] || return 1
  make_kubectl "$tmp/bin" || return 1
}

# Run the hook with the failures injected by $1, leaving merged output in
# $tmp/out, the invocations in $tmp/kubectl.log and the exit status in $rc. The
# status must not abort the test: it is itself one of the things under test.
# KUBECONFIG is neutered so a stub that stopped being found on PATH cannot reach
# a real cluster.
run_hook() {
  rc=0
  : > "$tmp/kubectl.log"
  KUBECONFIG=/dev/null \
  KUBECTL_LOG="$tmp/kubectl.log" \
  KUBECTL_FAIL="$1" \
  PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" > "$tmp/out" 2>&1 || rc=$?
  # The stub has to have been reached, or the assertions below measured
  # something other than this script driving kubectl.
  [ -s "$tmp/kubectl.log" ] || return 1
}

@test "a run where both waves completed reports success" {
  setup_case
  run_hook ""

  [ "$rc" = 0 ]
  grep -qF 'Cleanup completed successfully' "$tmp/out"
  # Both waves have to have run, or the success claim is about a script that
  # deleted nothing and the tests below have no baseline to differ from.
  grep -q 'tenantmodule!=true' "$tmp/kubectl.log"
  grep -q 'tenantmodule=true' "$tmp/kubectl.log"

  rm -rf "$tmp"
}

@test "every delete reaches kubectl with a wall-clock bound" {
  # The defect this pins. kubectl reads the default `--timeout=0` as wait
  # forever and substitutes a week, so a delete that arrives without the flag
  # hangs the hook for as long as any child HelmRelease cannot finalize — and
  # helm returns from the uninstall before removing anything, leaving the
  # release in storage to be retried. Read from the arguments the stub actually
  # received rather than from the template text, so a flag that stops being
  # passed cannot pass here by still being written somewhere in the file.
  setup_case
  run_hook ""

  deletes=$(grep -c 'delete helmreleases' "$tmp/kubectl.log" || true)
  [ "$deletes" = 2 ]
  # A bound of zero is the unbounded case spelled explicitly, so the value has
  # to start with a non-zero digit rather than merely be present.
  bounded=$(grep 'delete helmreleases' "$tmp/kubectl.log" |
    grep -c -- '--timeout=[1-9][0-9]*s' || true)
  if [ "$bounded" != "$deletes" ]; then
    echo "FAIL: $bounded of $deletes deletes carried a non-zero --timeout"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "the waits, the Job deadline and the uninstall budget stay in order" {
  # Four numbers, one ordering, and both ends of it are load-bearing.
  #
  # Waves under the deadline: activeDeadlineSeconds is the outer stop, and only
  # a backstop while the waits inside it can finish first. Raise a wave past the
  # remainder and the deadline starts cutting runs that were going to succeed.
  #
  # Deadline plus termination under the uninstall budget: flux takes that budget
  # from spec.uninstall.timeout / spec.timeout, and cozystack-api generates the
  # tenant HelmRelease with neither, so it is helm-controller's 300s default. The
  # deadline is measured from the Job's startTime and helm's budget from just
  # before it creates the Job, so at 300 helm gives up first — on a Job that is
  # still running, which is the state this whole change exists to end.
  #
  # The termination period belongs on the same side of that comparison as the
  # deadline. Since 1.31 the Job controller withholds the terminal condition
  # until every pod has finished terminating, so what helm waits for is the sum
  # rather than the deadline alone — and the sum is what is actually spent here,
  # because PID 1 is a shell running a multi-command script: it never execs into
  # kubectl, and a default disposition sent to PID 1 is discarded by the kernel
  # with no handler installed to catch it. Comparing the deadline alone would
  # call a Job that reports its failure at 300 correctly bounded at 270.
  UNINSTALL_BUDGET=300
  setup_case

  waits=0
  for seconds in $(grep -o -- '--timeout=[0-9]*s' "$tmp/s.sh" |
    sed -e 's|--timeout=||' -e 's|s$||'); do
    waits=$((waits + seconds))
  done
  # Non-zero, or the comparison below is satisfied by a script with no waits at
  # all to measure.
  [ "$waits" -gt 0 ]

  deadline=$(yq '.spec.activeDeadlineSeconds' "$tmp/job.yaml")
  # Checked before it is compared. Both comparisons below sit inside `if`, where
  # a status is a verdict rather than an error, so `[ 180 -ge null ]` reads as
  # "false" and a Job with no deadline at all passes every ordering check here.
  case "$deadline" in
    '' | *[!0-9]*)
      echo "FAIL: the Job carries no numeric activeDeadlineSeconds (read: '$deadline')"
      echo "      and then nothing bounds a run whose kubectl call never returns"
      return 1
      ;;
  esac
  if [ "$waits" -ge "$deadline" ]; then
    echo "FAIL: ${waits}s of bounded waits against an activeDeadlineSeconds of ${deadline}s"
    echo "      leaves nothing for scheduling, the image pull and the delete calls"
    return 1
  fi

  grace=$(yq '.spec.template.spec.terminationGracePeriodSeconds' "$tmp/job.yaml")
  # Read before it is added, for the same reason the deadline is: `null` here
  # would make the sum below a string comparison away from meaning nothing, and
  # an absent period is the 30s default rather than zero — the case that puts
  # the failure at exactly the budget.
  case "$grace" in
    '' | *[!0-9]*)
      echo "FAIL: the pod carries no numeric terminationGracePeriodSeconds (read: '$grace')"
      echo "      so it defaults to 30s and the Job reports its failure at ${deadline}s+30s"
      return 1
      ;;
  esac
  if [ "$((deadline + grace))" -ge "$UNINSTALL_BUDGET" ]; then
    echo "FAIL: an activeDeadlineSeconds of ${deadline}s plus a termination period of"
    echo "      ${grace}s reaches ${UNINSTALL_BUDGET}s of uninstall budget, so helm gives up"
    echo "      before the Job it is waiting on reports the failure"
    return 1
  fi

  rm -rf "$tmp"
}

@test "an applications wave that did not finish ends the run" {
  # Nothing stands behind these deletes, so a wave that gave up must not be
  # reported as done. The closing line is unreachable under `set -e`, and it has
  # to stay that way: a `|| echo` appended here would reinstate the exact defect
  # this file exists to stop — a cleanup that did not happen, claimed as one
  # that did, after which helm goes on to tear the tenant namespace down over
  # HelmReleases that are still there.
  setup_case
  run_hook "$WAVE_APPS"

  if [ "$rc" = 0 ]; then
    echo "FAIL: a wave that could not finish left the hook reporting success"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: the hook claimed success with applications still undeleted"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}

@test "an applications wave that did not finish does not go on to the modules" {
  # The ordering the two waves exist for. Tenant modules — etcd, monitoring,
  # ingress, seaweedfs, the gateway — are what the applications above run on,
  # and deleting them out from under applications that are still terminating
  # makes the namespace harder to remove, not easier.
  setup_case
  run_hook "$WAVE_APPS"

  # The first wave has to have been attempted, or the absence below is the
  # absence of a script that ran nothing.
  grep -q 'tenantmodule!=true' "$tmp/kubectl.log"
  if grep -q 'tenantmodule=true' "$tmp/kubectl.log"; then
    echo "FAIL: the tenant modules were deleted after the applications wave gave up"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a tenant modules wave that did not finish ends the run" {
  # The second wave is the last thing the script does, so a guard appended to it
  # would be invisible to every assertion about what runs afterwards. Only the
  # exit status and the closing line separate a swallowed failure from a real
  # completion here.
  setup_case
  run_hook "$WAVE_MODULES"

  # Both waves ran: the failure being injected is the second one, not the first.
  grep -q 'tenantmodule!=true' "$tmp/kubectl.log"
  grep -q 'tenantmodule=true' "$tmp/kubectl.log"
  if [ "$rc" = 0 ]; then
    echo "FAIL: a tenant modules wave that could not finish left the hook reporting success"
    cat "$tmp/out"
    return 1
  fi
  if grep -qF 'Cleanup completed successfully' "$tmp/out"; then
    echo "FAIL: the hook claimed success with tenant modules still undeleted"
    cat "$tmp/out"
    return 1
  fi

  rm -rf "$tmp"
}
