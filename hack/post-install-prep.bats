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
# Run with: bats hack/post-install-prep.bats
# -----------------------------------------------------------------------------

load test_helper

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
  # A failing exit status alone does not stop a loop that runs where set -e is
  # off, such as a function called under `if !`, so the guard also kills the
  # shell that called it.
  cat > "$d/bin/sleep" <<'STUB'
#!/bin/sh
now=$(cat "$STUB_CLOCK")
now=$(( now + ${1%%.*} ))
echo "$now" > "$STUB_CLOCK"
if [ "$now" -ge 5000 ]; then
  echo "STUB-RUNAWAY: virtual clock passed 5000s" >&2
  kill -TERM "$PPID"
  exit 1
fi
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
export STUB_BOUNDED=1
exec "$@"
STUB

  cat > "$d/bin/kubectl" <<'STUB'
#!/bin/sh
now=$(cat "$STUB_CLOCK")
printf '%s\n' "$*" >> "$STUB_CALLS"
# A call not run under `timeout`. `kubectl wait --timeout` does not count:
# that timeout starts after the GET that resolves the object, which carries no
# deadline of its own. Recorded on a line of its own so the assertions on the
# plain call lines above keep matching.
bounded=${STUB_BOUNDED:-}
[ -n "$bounded" ] || printf 'UNBOUNDED %s\n' "$*" >> "$STUB_CALLS"
case "$*" in
  'wait helmrelease/linstor '*)
    # The outer bound firing: `timeout` prints nothing and kubectl dies on the
    # signal before it can, so the status is the only trace. 124 on TERM, 137
    # when kubectl ignored it and the -k KILL landed.
    if [ -n "${STUB_WAIT_KILLED:-}" ]; then exit "$STUB_WAIT_KILLED"; fi
    if [ -n "${STUB_WAIT_ERR:-}" ]; then echo "$STUB_WAIT_ERR" >&2; fi
    [ "$STUB_HR_READY_AT" != never ] && [ "$now" -ge "$STUB_HR_READY_AT" ] ;;
  'wait deployment/linstor-controller '*)
    [ "$STUB_DEPLOY_READY_AT" != never ] && [ "$now" -ge "$STUB_DEPLOY_READY_AT" ] ;;
  'get endpoints '*) echo 192.0.2.11 ;;
  'get crd '*)
    if [ -n "${STUB_CRD_MISSING:-}" ]; then
      echo 'Error from server (NotFound): customresourcedefinitions.apiextensions.k8s.io "l2advertisements.metallb.io" not found' >&2
      exit 1
    fi ;;
  'get storageprofile local'*)
    if [ -n "${STUB_SP_MISSING:-}" ]; then
      echo 'Error from server (NotFound): storageprofiles.cdi.kubevirt.io "local" not found' >&2
      exit 1
    fi ;;
  'get hr -n cozy-metallb'*) echo "STUB-METALLB-HR metallb False install retries exhausted" ;;
  'get hr -A '*)
    echo "cozy-linstor linstor False dependency 'cozy-system/piraeus-operator' is not ready"
    echo "STUB-HR cozy-kubeovn kubeovn False install retries exhausted" ;;
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
  *'linstor sp l '*)
    # What the outer bound firing looks like from here: `timeout` prints
    # nothing and kubectl dies on the signal before it can, so the only
    # trace is the status.
    if [ -n "${STUB_SPL_KILLED:-}" ]; then
      exit 124
    fi
    # A remote command killed inside the container, OOM for one: kubectl
    # exec hands its status back, and 137 is also what `timeout -k` returns.
    if [ -n "${STUB_SPL_REMOTE_KILLED:-}" ]; then
      echo 'command terminated with exit code 137' >&2
      exit 137
    fi ;;
  *'linstor node list')
    i=0
    while [ "$i" -lt "${STUB_ONLINE:-3}" ]; do echo Online; i=$((i + 1)); done ;;
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

@test "every kubectl call on the success, container and every timeout path carries a bound" {
  # The caller blocks in `wait` on this script, so a single call that never
  # returns holds the install open until the job's own ceiling instead of
  # failing a step. A deadline checked between iterations does not help: the
  # check sits on the other side of the call that hangs. The timeout paths
  # matter most: their reads run only once the cluster is already unhealthy.
  for path in success container node-timeout hr-timeout metallb-timeout sp-timeout; do
    tmp=$(mktemp -d)
    prep_sandbox "$tmp"
    hr_ready=0
    case $path in
      node-timeout) export STUB_ONLINE=2 ;;
      # The only path that patches the CDI StorageProfile.
      container) export COZY_LINSTOR_DRBD_ENABLED=false ;;
      hr-timeout) hr_ready=never ;;
      metallb-timeout) export STUB_CRD_MISSING=1 ;;
      sp-timeout) export COZY_LINSTOR_DRBD_ENABLED=false STUB_SP_MISSING=1 ;;
    esac

    run_prep "$tmp" "$hr_ready" 0
    unset STUB_ONLINE COZY_LINSTOR_DRBD_ENABLED STUB_CRD_MISSING STUB_SP_MISSING

    case $path in
      success|container) [ "$(cat "$tmp/rc")" -eq 0 ] ;;
      *)
        [ "$(cat "$tmp/rc")" -ne 0 ]
        grep -q 'timed out' "$tmp/err" ;;
    esac
    if grep '^UNBOUNDED ' "$tmp/calls" >&2; then
      echo "unbounded kubectl calls on the $path path" >&2
      return 1
    fi
    # A plain request keeps a client budget strictly inside the wall clock one,
    # so kubectl names why it could not finish before the kill lands.
    grep '^timeout dur=.* -- kubectl \(get\|apply\|patch\|events\) ' "$tmp/calls" > "$tmp/reads" || true
    [ -s "$tmp/reads" ]
    while IFS= read -r line; do
      outer=${line#timeout dur=}
      outer=${outer%% *}
      inner=$(printf '%s' "$line" | sed -n 's/.*--request-timeout=\([0-9]*\)s.*/\1/p')
      if [ -z "$inner" ] || [ "$inner" -ge "$outer" ]; then
        echo "client budget not inside the wall clock one: $line" >&2
        return 1
      fi
    done < "$tmp/reads"
    rm -rf "$tmp"
  done
}

@test "a client error in the wait loop reaches the log instead of a bare timeout" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  # Exits non-zero at once, like NotFound, so by status alone the loop cannot
  # tell it from an object that is not created yet. Without the text the
  # timeout names the HelmRelease while the cause sits in the client.
  export STUB_WAIT_ERR='error: You must be logged in to the server (Unauthorized)'
  run_prep "$tmp" never 0
  unset STUB_WAIT_ERR

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'linstor HelmRelease to be Ready' "$tmp/err"
  grep -q 'Unauthorized' "$tmp/err"
  rm -rf "$tmp"
}

@test "a NotFound repeated on every attempt is printed once, not every poll" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  export STUB_WAIT_ERR='Error from server (NotFound): helmreleases.helm.toolkit.fluxcd.io "linstor" not found'
  run_prep "$tmp" never 0
  unset STUB_WAIT_ERR

  [ "$(cat "$tmp/rc")" -ne 0 ]
  # The timeout line repeats the last error; outside it, one line in ~180
  # attempts. Printing every attempt would bury the rest of the log.
  n=$(grep -v 'timed out' "$tmp/err" | grep -c 'not found' || true)
  [ "$n" -eq 1 ]
  grep 'timed out' "$tmp/err" | grep -q 'not found'
  # Printed on every green run too, since the release appears ~70s in, so it
  # has to read as this script's progress rather than as a failure.
  grep -v 'timed out' "$tmp/err" | grep 'not found' | grep -q '^\[post-install-prep\] '
  rm -rf "$tmp"
}

@test "a wait attempt cut by its outer bound says so in the timeout line" {
  for code in 124 137; do
    tmp=$(mktemp -d)
    prep_sandbox "$tmp"

    export STUB_WAIT_KILLED=$code
    run_prep "$tmp" never 0
    unset STUB_WAIT_KILLED

    [ "$(cat "$tmp/rc")" -ne 0 ]
    # Without this the line reads "last kubectl error: none", which says the
    # client had nothing to report, not that it was killed mid-request.
    grep 'timed out' "$tmp/err" | grep -q 'cut by its outer bound'
    rm -rf "$tmp"
  done
}

@test "a linstor HelmRelease timeout names the upstream release that blocks it" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  run_prep "$tmp" never 0

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'linstor HelmRelease to be Ready' "$tmp/err"
  # The linstor release is usually the victim of a dependency chain, not the
  # cause, and the cozy-linstor pods say nothing about kubeovn. The release
  # list carries every Ready message, so the blocking release is named in the
  # same log that reports the timeout.
  grep -q 'STUB-HR cozy-kubeovn kubeovn' "$tmp/err"
  rm -rf "$tmp"
}

@test "a MetalLB CRD wait that runs out says so and shows which CRD is missing" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  export STUB_CRD_MISSING=1
  run_prep "$tmp" 0 0
  unset STUB_CRD_MISSING

  [ "$(cat "$tmp/rc")" -ne 0 ]
  # A silent exit here leaves the "waiting for" line as the last word, which
  # cannot tell CRDs that are late from an operator that never got that far.
  grep -q 'timed out after 300s waiting for MetalLB CRDs' "$tmp/err"
  grep -q 'l2advertisements.metallb.io" not found' "$tmp/err"
  grep -q 'STUB-METALLB-HR' "$tmp/err"
  final=$(cat "$tmp/clock")
  [ "$final" -ge 300 ]
  [ "$final" -le 302 ]
  rm -rf "$tmp"
}

@test "a CDI StorageProfile wait that runs out says so before any patch" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  export COZY_LINSTOR_DRBD_ENABLED=false STUB_SP_MISSING=1
  run_prep "$tmp" 0 0
  unset COZY_LINSTOR_DRBD_ENABLED STUB_SP_MISSING

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'timed out after 600s waiting for CDI StorageProfile/local' "$tmp/err"
  grep -q 'storageprofiles.cdi.kubevirt.io "local" not found' "$tmp/err"
  if grep -q '^patch storageprofile' "$tmp/calls"; then
    echo "the StorageProfile was patched although it never appeared" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "container mode waits for the CDI StorageProfile before patching it" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  export COZY_LINSTOR_DRBD_ENABLED=false
  run_prep "$tmp" 0 0
  unset COZY_LINSTOR_DRBD_ENABLED

  [ "$(cat "$tmp/rc")" -eq 0 ]
  # CDI creates the profile asynchronously; a patch sent first fails on a
  # profile that does not exist yet.
  got=$(grep -n '^get storageprofile local' "$tmp/calls" | head -1 | cut -d: -f1)
  patched=$(grep -n '^patch storageprofile local' "$tmp/calls" | head -1 | cut -d: -f1)
  [ -n "$got" ]
  [ -n "$patched" ]
  [ "$got" -lt "$patched" ]
  rm -rf "$tmp"
}

@test "a storage pool listing killed by its bound stops before any pool is created and says so" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  # Read as "no pools exist", a failed listing sends a create for every node,
  # and the ones that do exist fail it with a reason that is not the cause.
  export STUB_SPL_KILLED=1
  run_prep "$tmp" 0 0
  unset STUB_SPL_KILLED

  [ "$(cat "$tmp/rc")" -ne 0 ]
  # Nothing else in the log would say why the script stopped.
  grep 'killed after 300s' "$tmp/err" | grep -q 'linstor sp l'
  if grep -q 'create-device-pool' "$tmp/calls"; then
    echo "pools were created after the listing failed" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a listing whose remote command was killed is not reported as the bound firing" {
  tmp=$(mktemp -d)
  prep_sandbox "$tmp"

  export STUB_SPL_REMOTE_KILLED=1
  run_prep "$tmp" 0 0
  unset STUB_SPL_REMOTE_KILLED

  [ "$(cat "$tmp/rc")" -ne 0 ]
  grep -q 'command terminated with exit code 137' "$tmp/err"
  if grep -q 'killed after' "$tmp/err"; then
    echo "a remote kill was blamed on the call bound" >&2
    return 1
  fi
  rm -rf "$tmp"
}
