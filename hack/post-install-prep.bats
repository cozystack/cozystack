#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the wait budgets in hack/e2e-post-install-prep.sh.
#
# The script drives a serial reconcile chain -- cozystack-operator -> platform
# HR -> linstor HR -> piraeus-operator -> cert-manager issues the controller
# TLS -> linstor-controller Deployment -- and every link of it is waited on in
# order. What these tests pin is how the waits are budgeted: each link gets its
# own window measured from the moment the link before it completed, so a long
# but healthy head cannot starve the tail, while a link that never converges
# still fails inside one window rather than running forever.
#
# Strategy: the script is run end to end against a stub PATH carrying a virtual
# clock. `date` reads a counter file, `sleep` advances it, and `kubectl` decides
# each wait from that same counter -- so a 15-minute budget elapses in a few
# hundred forks and the timings are exact rather than wall-clock approximate.
# The stub also records every kubectl invocation, which is how the timeout
# diagnostics are asserted. Mock IPs use the RFC 5737 documentation range.
#
# The convergence times fed in are the ones measured on the two runs that
# motivated this: the linstor HelmRelease went Ready 452s after the script
# started, and the Deployment became Available a further ~520s later.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#   - A line that is exactly `}` is rewritten, so stub bodies below use `case`
#     rather than nested functions.
#
# Run with: hack/cozytest.sh hack/post-install-prep.bats
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-post-install-prep.sh"

# prep_sandbox <dir> -- lay down the stub PATH and the virtual clock.
#
# STUB_HR_READY_AT / STUB_DEPLOY_READY_AT are virtual-clock seconds at which
# the corresponding `kubectl wait` starts succeeding, or the string `never`.
# Every other kubectl call succeeds, so the script's post-LINSTOR tail (storage
# pools, StorageClasses, MetalLB) runs through without a cluster.
prep_sandbox() {
  d=$1
  mkdir -p "$d/bin"
  echo 0 > "$d/clock"
  : > "$d/calls"

  cat > "$d/bin/date" <<'STUB'
#!/bin/sh
cat "$STUB_CLOCK"
STUB

  # Advancing the clock in `sleep` is what makes a 900s budget cost ~180 forks
  # instead of 15 minutes. The ceiling is a runaway guard: if the script under
  # test loses its deadline check entirely, the wait loop would otherwise spin
  # forever, and a hung unit test is worse than a failing one. It has to stay
  # low enough to trip in seconds -- a guard that itself takes minutes to fire
  # is the hang it was meant to prevent -- and above every budget under test.
  cat > "$d/bin/sleep" <<'STUB'
#!/bin/sh
now=$(cat "$STUB_CLOCK")
now=$(( now + ${1%%.*} ))
echo "$now" > "$STUB_CLOCK"
[ "$now" -lt 5000 ]
STUB

  # Records the parsed bound and then runs the real command, so a wrapped call
  # still reaches the kubectl stub and the existing call assertions hold. The
  # duration is parsed here rather than in the test because `timeout [-k G] N`
  # puts two numbers on the line and only the second one is the wall clock.
  cat > "$d/bin/timeout" <<'STUB'
#!/bin/sh
grace=""
dur=""
while [ $# -gt 0 ]; do
  case $1 in
    -k) grace=$2; shift 2 ;;
    [0-9]*) dur=$1; shift; break ;;
    *) break ;;
  esac
done
printf 'timeout dur=%s grace=%s -- %s\n' "$dur" "$grace" "$*" >> "$STUB_CALLS"
exec "$@"
STUB

  cat > "$d/bin/kubectl" <<'STUB'
#!/bin/sh
now=$(cat "$STUB_CLOCK")
printf '%s\n' "$*" >> "$STUB_CALLS"
case "$*" in
  'wait helmrelease/linstor '*)
    [ "$STUB_HR_READY_AT" != never ] && [ "$now" -ge "$STUB_HR_READY_AT" ] ;;
  'wait deployment/linstor-controller '*)
    [ "$STUB_DEPLOY_READY_AT" != never ] && [ "$now" -ge "$STUB_DEPLOY_READY_AT" ] ;;
  'get endpoints '*) echo 192.0.2.11 ;;
  'get pods '*)
    if [ -n "${STUB_PODS_FAIL:-}" ]; then
      echo "Error from server (Forbidden): pods is forbidden" >&2
      exit 1
    fi
    echo "STUB-POD linstor-controller 0/1 Init:CrashLoopBackOff" ;;
  'events '*|'get events '*)
    # Model the failure the real sorter has: an Event written through
    # events.k8s.io/v1 reaches the core API with .lastTimestamp unset, and
    # kubectl fails the whole read rather than skipping that item. A
    # diagnostic that dies this way prints nothing, which reads exactly like
    # a namespace with no events -- so the assertion below is on the content
    # arriving, not on the call being made.
    case "$*" in
      *--sort-by=.lastTimestamp*)
        echo "error: couldn't find any field with path {.lastTimestamp}" >&2
        exit 1 ;;
    esac
    # Any other reason the read can fail -- RBAC, a wedged apiserver, a
    # renamed namespace. Kept separate from the sort-key case above so the
    # cause and the consequence are pinned by different tests.
    if [ -n "${STUB_EVENTS_FAIL:-}" ]; then
      echo "Error from server (Forbidden): events is forbidden" >&2
      exit 1
    fi
    echo "STUB-EVENT MountVolume.SetUp failed for volume client-tls" ;;
  *'linstor node list') printf 'Online\nOnline\nOnline\n' ;;
  *) exit 0 ;;
esac
STUB

  chmod +x "$d/bin/date" "$d/bin/sleep" "$d/bin/kubectl" "$d/bin/timeout"
}

# run_prep <dir> <hr-ready-at> <deploy-ready-at> [events-fail] [pods-fail]
# Runs the script under the stubs, capturing stdout, stderr and the exit status
# without tripping set -e. The last two are non-empty to make the corresponding
# diagnostic read fail; pass an empty string for the fourth to reach the fifth.
run_prep() {
  d=$1
  STUB_CLOCK="$d/clock" STUB_CALLS="$d/calls" \
  STUB_HR_READY_AT=$2 STUB_DEPLOY_READY_AT=$3 \
  STUB_EVENTS_FAIL="${4:-}" STUB_PODS_FAIL="${5:-}" \
  PATH="$d/bin:$PATH" \
    "$SCRIPT" > "$d/out" 2> "$d/err" && echo 0 > "$d/rc" || echo $? > "$d/rc"
}

@test "the Deployment wait keeps its full budget after a slow HelmRelease wait" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" 452 972

  cat "$tmp/err" >&2
  [ "$(cat "$tmp/rc")" -eq 0 ]
  grep -q '\[post-install-prep\] done' "$tmp/out"
  # The Deployment converged past the point where a single chain-wide 15m
  # window anchored at script start would already have expired.
  [ "$(cat "$tmp/clock")" -ge 972 ]
  rm -rf "$tmp"
}

@test "a Deployment that never becomes Available fails inside one budget" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" 0 never

  [ "$(cat "$tmp/rc")" -ne 0 ]
  # One budget, not two and not unbounded: the wait started at 0 and gave up
  # at 900. The upper bound allows a single 5s poll interval beyond that and
  # nothing more, so a second budget would fail it.
  final=$(cat "$tmp/clock")
  [ "$final" -ge 900 ]
  [ "$final" -le 905 ]
  rm -rf "$tmp"
}

@test "the Deployment timeout reports the linstor pods and events" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" 0 never

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'timed out' "$tmp/err"
  grep -q 'linstor-controller Deployment to be Available' "$tmp/err"
  # Which link was actually blocking -- a crash-looping init container versus a
  # Secret cert-manager has not issued yet -- is only visible in the pods and
  # the namespace events, so the timeout has to read both.
  grep -q '^get pods -n cozy-linstor' "$tmp/calls"
  grep -q '^events -n cozy-linstor' "$tmp/calls"
  # And their content has to reach the log, not merely be asked for: a read
  # that fails prints nothing, which is the same shape as a namespace with
  # nothing wrong in it.
  grep -q 'STUB-POD' "$tmp/err"
  grep -q 'STUB-EVENT' "$tmp/err"
  # Both reads carry a wall-clock bound, and the client budget inside each is
  # strictly smaller than it. Equal values would put the outer kill first, so
  # kubectl would never get to say why it could not read -- which is the whole
  # point of reading at all on this path.
  for what in 'get pods' 'events'; do
    line=$(grep "^timeout dur=.* kubectl $what -n cozy-linstor" "$tmp/calls" | head -1)
    [ -n "$line" ]
    outer=${line#timeout dur=}
    outer=${outer%% *}
    inner=$(printf '%s' "$line" | sed -n 's/.*--request-timeout=\([0-9]*\)s.*/\1/p')
    [ -n "$outer" ]
    [ -n "$inner" ]
    [ "$inner" -lt "$outer" ]
  done
  rm -rf "$tmp"
}

@test "an events read that fails names its reason instead of going quiet" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" 0 never fail-events

  [ "$(cat "$tmp/rc")" -ne 0 ]
  # Silence here would be read as "the namespace had nothing to say", which is
  # the opposite of what happened. The reason has to survive to the log.
  grep -q 'Forbidden' "$tmp/err"
  rm -rf "$tmp"
}

@test "a leg that fails does not take the other leg down with it" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  # The pods read runs first, so failing it is the only way to ask whether the
  # events read still happens. Failing the events read instead proves nothing:
  # the pods output has already been emitted by then either way.
  run_prep "$tmp" 0 never '' fail-pods

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'Forbidden' "$tmp/err"
  grep -q 'STUB-EVENT' "$tmp/err"
  rm -rf "$tmp"
}

@test "a HelmRelease that never goes Ready fails before the Deployment is waited on" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" never 0

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'linstor HelmRelease to be Ready' "$tmp/err"
  final=$(cat "$tmp/clock")
  [ "$final" -ge 900 ]
  [ "$final" -le 905 ]
  if grep -q '^wait deployment/linstor-controller' "$tmp/calls"; then
    echo "the Deployment was waited on after the HelmRelease wait failed" >&2
    return 1
  fi
  rm -rf "$tmp"
}
