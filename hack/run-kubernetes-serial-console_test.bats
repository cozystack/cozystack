#!/usr/bin/env bats
# Regression coverage for tenant-worker guest serial console capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# The capture exists for the one failure class no other collector reaches: a
# worker that stalls in the guest before Talos apid answers. Its defining
# property is therefore what it does NOT need — no talosctl, no reader
# certificate, no helper Pod, nothing inside the guest. These tests pin that
# property, because a capture that grew an in-guest dependency would still look
# healthy while being blind to exactly the runs it was written for.

kubectl_calls=/dev/null
kubectl_pod_names=
kubectl_list_rc=0
kubectl_list_stderr=
kubectl_init_names=
kubectl_init_rc=0
kubectl_wedge_rows=
kubectl_wedge_rc=0
kubectl_wedge_stderr=
mktemp_fail=0
kubectl_logs_output='mock console output'
kubectl_logs_rc=0
kubectl_logs_stderr=
kubectl_timeline_rows='name=virt-launcher-worker-a-11111 phase=Running podStartTime=2026-08-15T14:35:26Z computeStartedAt=2026-08-15T14:35:47Z consoleStartedAt=2026-08-15T14:35:47Z'
kubectl_timeline_rc=0
kubectl_timeline_stderr=
talosctl_calls=/dev/null
timeout_calls=/dev/null
timeout_fail_pod=
date_now=

date() {
  # Same shape the node-join suite uses: a fixed clock so the passing path's
  # remaining-time arithmetic is decided by the test rather than by when it ran.
  if [ -n "${date_now}" ] && [ "${1:-}" = +%s ]; then
    printf '%s\n' "${date_now}"
    return 0
  fi
  command date "$@"
}

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"

  if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then
    case "$*" in
      *computeStartedAt*)
        # Rows first, status after, so a cut-off read can be modelled as what it
        # is: a prefix that arrived plus a non-zero status. Returning early on
        # the status instead would make "failed" and "returned nothing" the same
        # scenario, and the branch that tells them apart untestable.
        #
        # stderr is staged separately for the same reason every other arm here
        # stages it: kubectl writes warnings beside a healthy read as well as
        # errors beside a failed one, and which file those land in is decided by
        # the status rather than by anything in the text.
        [ -z "${kubectl_timeline_stderr}" ] || printf '%s\n' "${kubectl_timeline_stderr}" >&2
        [ -z "${kubectl_timeline_rows}" ] || printf '%s\n' "${kubectl_timeline_rows}"
        return "${kubectl_timeline_rc}"
        ;;
      *initContainerStatuses*)
        [ -z "${kubectl_wedge_stderr}" ] || printf '%s\n' "${kubectl_wedge_stderr}" >&2
        [ "${kubectl_wedge_rc}" -eq 0 ] || return "${kubectl_wedge_rc}"
        [ -z "${kubectl_wedge_rows}" ] || printf '%s\n' "${kubectl_wedge_rows}"
        return 0
        ;;
      *initContainers*)
        [ "${kubectl_init_rc}" -eq 0 ] || return "${kubectl_init_rc}"
        [ -z "${kubectl_init_names}" ] || printf '%s\n' "${kubectl_init_names}"
        return 0
        ;;
    esac
    [ -z "${kubectl_list_stderr}" ] || printf '%s\n' "${kubectl_list_stderr}" >&2
    [ "${kubectl_list_rc}" -eq 0 ] || return "${kubectl_list_rc}"
    [ -z "${kubectl_pod_names}" ] || printf '%s\n' ${kubectl_pod_names}
    return 0
  fi

  if [ "${3:-}" = logs ]; then
    [ -z "${kubectl_logs_stderr}" ] || printf '%s\n' "${kubectl_logs_stderr}" >&2
    [ "${kubectl_logs_rc}" -eq 0 ] || return "${kubectl_logs_rc}"
    [ -z "${kubectl_logs_output}" ] || printf '%s for %s\n' "${kubectl_logs_output}" "${4:-}"
    return 0
  fi

  return 0
}

talosctl() {
  printf '%s\n' "$*" >>"${talosctl_calls}"
}

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  [ "${1:-}" = -k ] || return 97
  shift 3
  "$@" || command_rc=$?
  if [ -n "${timeout_fail_pod}" ]; then
    case " $* " in
      *" ${timeout_fail_pod} "*) return 124 ;;
    esac
  fi
  return "${command_rc}"
}

mktemp() {
  # The scratch file the wedge check allocates is the one failure the function
  # cannot report through kubectl, so it needs its own knob. `-d` is left
  # working because the tests themselves use it for their own temp dirs.
  if [ "${mktemp_fail}" = 1 ] && [ "${1:-}" != -d ]; then
    return 1
  fi
  command mktemp "$@"
}

assert_file_contains() {
  local needle="$1"
  local file="$2"

  case "$(cat "${file}")" in
    *"${needle}"*) return 0 ;;
  esac
  printf 'expected %s to contain: %s\n' "${file}" "${needle}" >&2
  return 1
}

assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"

  # A missing file must fail rather than vacuously pass: awk exits 2 on an
  # unreadable path, which is indistinguishable from "no line matched" once the
  # status is folded into an if.
  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  if awk -v pattern="${pattern}" '$0 ~ pattern { found = 1 } END { exit found ? 0 : 1 }' "${file}"; then
    printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
}

# The function under test redirects a command's stderr into a report file and
# decides which artifact to write from it. cozytest.sh runs under `set -x`, so
# the tracer's own line for that command is emitted on the very stderr being
# redirected, and every assertion about those files would be reading the
# tracer. Call through this wrapper so the sinks hold what production would
# put in them.
run_capture() {
  local _rc=0
  ( set +x; cozy_capture_tenant_serial_console "${1:-capture staged by a unit test}" "${2:-6}" ) || _rc=$?
  return "${_rc}"
}

@test "a wedged console container is named in the headline, not buried" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false PodInitializing virt-launcher-worker-a-11111\nfalse PodInitializing virt-launcher-worker-b-22222')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # If kubevirt/kubevirt#15989 still bites, the suite's own headline reads
  # "fewer than 2 tenant nodes Ready within 29m" -- byte-identical to the
  # failure this instrumentation exists to study. A triager would file the
  # experiment's answer as the known flake. The diagnostic has to name its
  # own worst case before the noise starts.
  assert_file_contains 'guest-console-log' "$tmp/out"
  assert_file_contains '15989' "$tmp/out"
  assert_file_contains 'virt-launcher-worker-a-11111' "$tmp/out"
  rm -rf "$tmp"
}

@test "a read that never answered is not reported as a healthy console" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rc=1
  kubectl_wedge_rows="$(printf 'false PodInitializing virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # The guard against a false headline must not become a false all-clear.
  # An apiserver blip here would otherwise leave the reader with no line at
  # all, which reads as "the console container is fine" -- nothing was
  # checked. Same rule the sibling attach check follows.
  assert_file_lacks_pattern '15989' "$tmp/out"
  assert_file_contains 'unknown, not fine' "$tmp/out"
  rm -rf "$tmp"
}

@test "the wedge row carries the waiting reason, not just the ready bit" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false PodInitializing virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # ready=false on a sidecar means "not running", which a terminated Pod
  # satisfies as well as a wedged one. The reason keeps the two apart.
  assert_file_contains 'PodInitializing' "$tmp/out"
  assert_file_contains 'state.waiting.reason' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the taxonomy does not claim to be exhaustive" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false OOMKilled virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # Two named reasons do not cover the space -- OOMKilled and ContainerCreating
  # reach this line too, and a superseded Pod killed past its grace period
  # reads Error. A headline that enumerates two and stops implies the rest
  # cannot happen, which is the over-claim this whole line was rewritten to
  # stop making.
  assert_file_contains 'any other reason, or none, is not covered' "$tmp/out"
  rm -rf "$tmp"
}

@test "the wedge taxonomy names CrashLoopBackOff as the bug, not as a superseded Pod" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false CrashLoopBackOff virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # The sidecar restarts on failure, so the hang settles into CrashLoopBackOff
  # and passes through Error. Filing those under "superseded Pod" would hand a
  # triager the experiment's own failure labelled as noise -- the precise
  # inversion this function exists to prevent.
  assert_file_contains 'CrashLoopBackOff or Error below is the wedge shape' "$tmp/out"
  assert_file_lacks_pattern 'Completed or Error below means a superseded' "$tmp/out"
  rm -rf "$tmp"
}

@test "a superseded Pod is labelled, not called a wedge" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false Completed virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # ContainerState is a union, so a waiting reason alone is blank for the
  # terminated case and cannot separate it from anything else at ready=false.
  # The terminated reason is what labels the superseded Pod, and Completed is
  # the shape that means superseded rather than wedged.
  assert_file_contains 'Completed' "$tmp/out"
  assert_file_contains 'state.terminated.reason' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the headline points at the describe rather than concluding" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false  virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # The bit cannot separate every shape, so the line must not assert one. A
  # headline that concludes more than it established is the same mislabel this
  # function exists to prevent, pointing the other way.
  assert_file_contains 'if these are current Pods' "$tmp/out"
  assert_file_contains 'describe that follows settles it' "$tmp/out"
  rm -rf "$tmp"
}

@test "a scratch file that could not be allocated is not silence either" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'false CrashLoopBackOff virt-launcher-worker-a-11111')"
  mktemp_fail=1

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1
  mktemp_fail=0

  # The function argues twelve lines further down that silence would read as
  # "the console container is fine" when nothing was checked. That argument
  # does not stop applying one branch earlier.
  assert_file_contains 'unknown, not fine' "$tmp/out"
  assert_file_lacks_pattern '15989' "$tmp/out"
  rm -rf "$tmp"
}

@test "a failed read keeps the reason it failed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rc=1
  kubectl_wedge_stderr="Error from server: etcdserver: leader changed"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # Discarding stderr here would report a bare exit code for a failure whose
  # cause kubectl already named, on the one path that exists to stop a reader
  # guessing. Same rule the sibling attach check follows.
  assert_file_contains 'leader changed' "$tmp/out"
  rm -rf "$tmp"
}

@test "a healthy console container says nothing in the headline" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_wedge_rows="$(printf 'true  virt-launcher-worker-a-11111')"

  ( set +x; cozy_report_guest_console_wedge ) >"$tmp/out" 2>&1

  # A marker that fires on healthy runs is one a reader learns to skip.
  assert_file_lacks_pattern '15989' "$tmp/out"
  rm -rf "$tmp"
}

@test "the wedge check runs before the rest of the node-join diagnostics" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  head=$(grep -n 'node-join failed: fewer than 2 tenant nodes Ready' "$lib" | head -n 1 | cut -d: -f1)
  wedge=$(grep -n '^ *cozy_report_guest_console_wedge || true$' "$lib" | head -n 1 | cut -d: -f1)
  first=$(grep -n 'describe nodes' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$wedge" ]; then
    echo "expected the node-join failure path to check for a wedged console container" >&2
    return 1
  fi
  if [ "$wedge" -le "$head" ] || [ "$wedge" -ge "$first" ]; then
    echo "the wedge check (line $wedge) must sit between the headline ($head) and the first diagnostic ($first)" >&2
    return 1
  fi
}

@test "console capture needs nothing from inside the guest" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-smoke

  run_capture

  assert_file_contains 'logs virt-launcher-worker-a-11111 -c guest-console-log' "$kubectl_calls"
  assert_file_lacks_pattern 'certificate|exec|apply|talosconfig' "$kubectl_calls"
  [ ! -s "$talosctl_calls" ]
  assert_file_contains 'mock console output' "$COZY_REPORT_DIR/snapshots/console-smoke/tenant-serial-console/virt-launcher-worker-a-11111.log"
  rm -rf "$tmp"
}

@test "each read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-bounded

  run_capture

  assert_file_contains '-k 5 30 kubectl -n tenant-test logs virt-launcher-worker-a-11111 -c guest-console-log' "$timeout_calls"
  assert_file_contains '[capture exit code: 0]' "$COZY_REPORT_DIR/snapshots/console-bounded/tenant-serial-console/virt-launcher-worker-a-11111.log"
  rm -rf "$tmp"
}

@test "the read keeps the boot output instead of the tail" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-head

  run_capture

  # A guest that stalls before apid is diagnosed from what it printed while
  # booting, and a guest that instead repaints the console indefinitely would
  # push that boot output out of any --tail window. --limit-bytes truncates
  # from the far end instead, which keeps whatever the kubelet still holds.
  # It cannot recover what container-log rotation already dropped.
  assert_file_contains '--limit-bytes=' "$kubectl_calls"
  assert_file_lacks_pattern '--tail' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a pod whose read times out does not stop the next pod" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111 virt-launcher-worker-b-22222"
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-partial

  run_capture

  assert_file_contains '[capture exit code: 124]' "$COZY_REPORT_DIR/snapshots/console-partial/tenant-serial-console/virt-launcher-worker-a-11111.log"
  assert_file_contains '[capture exit code: 0]' "$COZY_REPORT_DIR/snapshots/console-partial/tenant-serial-console/virt-launcher-worker-b-22222.log"
  rm -rf "$tmp"
}

@test "an empty virt-launcher list is recorded rather than passed over" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-empty

  capture_rc=0
  run_capture || capture_rc=$?

  [ "$capture_rc" -eq 1 ]
  assert_file_contains 'no virt-launcher Pod found' "$COZY_REPORT_DIR/snapshots/console-empty/tenant-serial-console/setup-error.log"
  assert_file_lacks_pattern 'guest-console-log' "$timeout_calls"
  rm -rf "$tmp"
}

@test "a failed Pod listing is recorded rather than passed over" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_list_rc=1
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-list-error

  capture_rc=0
  run_capture || capture_rc=$?

  [ "$capture_rc" -eq 1 ]
  assert_file_contains 'failed to list tenant virt-launcher Pods' "$COZY_REPORT_DIR/snapshots/console-list-error/tenant-serial-console/setup-error.log"
  assert_file_lacks_pattern 'guest-console-log' "$timeout_calls"
  rm -rf "$tmp"
}

@test "a warning on a successful listing is kept apart from a setup failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_list_stderr="W0807 partial results warning"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-warn

  run_capture

  # kubectl writes to stderr and exits 0 for warnings and for "No resources
  # found" alike, so the exit status decides the file. A warning filed as a
  # setup error reads as a broken collector on an otherwise healthy run.
  assert_file_contains 'partial results warning' "$COZY_REPORT_DIR/snapshots/console-warn/tenant-serial-console/READ-WARNINGS.txt"
  [ ! -f "$COZY_REPORT_DIR/snapshots/console-warn/tenant-serial-console/setup-error.log" ]
  rm -rf "$tmp"
}

@test "a healthy listing leaves no warnings file behind" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-quiet

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/console-quiet/tenant-serial-console/READ-WARNINGS.txt" ]
  [ ! -f "$COZY_REPORT_DIR/snapshots/console-quiet/tenant-serial-console/setup-error.log" ]
  rm -rf "$tmp"
}

@test "a failed listing keeps the stderr that explains it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_list_rc=1
  kubectl_list_stderr="Error from server (Forbidden): pods is forbidden"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-forbidden

  capture_rc=0
  run_capture || capture_rc=$?

  [ "$capture_rc" -eq 1 ]
  assert_file_contains 'Forbidden' "$COZY_REPORT_DIR/snapshots/console-forbidden/tenant-serial-console/setup-error.log"
  rm -rf "$tmp"
}

@test "the attach check passes when the console container is present" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111=guest-console-log"

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?

  [ "$attach_rc" -eq 0 ]
  rm -rf "$tmp"
}

@test "a matched Pod with no init containers at all is the finding, not a miss" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111="

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?

  # This is the shape an inert setting actually produces here: guest-console-log
  # is the only init container these Pods ever get, so "the field did not take
  # effect" means initContainers is absent, not that some other name appears.
  # Reading a bare list of container names would render it byte-identical to
  # "no Pod matched" and let the suite pass over the one thing this checks.
  [ "$attach_rc" -eq 2 ]
  rm -rf "$tmp"
}

@test "a read that never answered is not reported as an inert setting" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_rc=1

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) 2>"$tmp/err" || attach_rc=$?

  # Piping the read into grep would collapse this onto the absent-container
  # branch: no output, grep fails, and an API blip gets published as "the
  # override did not take effect" -- a cause nothing established. The message
  # is asserted, not just the code: a failed read and a read that matched
  # nothing share an exit code but are different facts, and only the status
  # tells them apart.
  [ "$attach_rc" -eq 1 ]
  assert_file_contains 'could not read tenant virt-launcher Pods' "$tmp/err"
  rm -rf "$tmp"
}

@test "a read that matched no Pod is not reported as an inert setting either" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names=

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) 2>"$tmp/err" || attach_rc=$?

  # A selector that matched nothing says nothing about any Pod's containers,
  # so it belongs with the failed read rather than with the absent container.
  [ "$attach_rc" -eq 1 ]
  assert_file_contains 'no tenant virt-launcher Pod matched' "$tmp/err"
  rm -rf "$tmp"
}

@test "the absent-container diagnostics are bounded like the check itself" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111="

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?

  [ "$attach_rc" -eq 2 ]
  # These run on a path that ends in exit 1; an unbounded hang here would take
  # the step's deadline and the tenant snapshot the exit is meant to reach.
  [ "$(grep -c 'get virtualmachineinstances' "$timeout_calls")" -eq 1 ]
  [ "$(grep -c 'get pods' "$timeout_calls")" -eq 2 ]
  rm -rf "$tmp"
}

@test "a silent read records whether the container ever started" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-silent

  run_capture

  # An empty console log has two causes that look identical in the artifact:
  # the guest printed nothing, or the container never started so there was no
  # stream at all. The second is what kubevirt/kubevirt#15989 produces, and it
  # is the reading that would otherwise be filed as "the guest was silent".
  # The Pod's own state is the only thing that separates them.
  d="$COZY_REPORT_DIR/snapshots/console-silent/tenant-serial-console"
  assert_file_contains 'describe pod' "$d/POD-STATE.txt"
  assert_file_contains 'events' "$d/POD-STATE.txt"
  assert_file_contains 'returned nothing' "$d/virt-launcher-worker-a-11111.log"
  rm -rf "$tmp"
}

@test "a container that never started is not called truncated or silent" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_output=
  kubectl_logs_rc=1
  kubectl_logs_stderr="Error from server (BadRequest): container guest-console-log is not valid for pod virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-absent

  run_capture

  # This is the shape kubevirt/kubevirt#15989 produces and the headline case
  # for the whole collector. Folding stderr into the capture would make the
  # file non-empty and the console would read as having said something;
  # nothing was truncated, because nothing was ever read.
  d="$COZY_REPORT_DIR/snapshots/console-absent/tenant-serial-console"
  assert_file_contains 'no console at all' "$d/virt-launcher-worker-a-11111.log"
  assert_file_lacks_pattern 'truncated' "$d/virt-launcher-worker-a-11111.log"
  assert_file_contains 'is not valid for pod' "$d/virt-launcher-worker-a-11111.read-error.log"
  [ -f "$d/POD-STATE.txt" ]
  rm -rf "$tmp"
}

@test "a read killed without a word does not point at a missing file" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_output=
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-killed

  run_capture

  # timeout kills the child without a word, so both streams can be empty.
  # Naming read-error.log then sends the reader after evidence that was
  # never written.
  d="$COZY_REPORT_DIR/snapshots/console-killed/tenant-serial-console"
  [ ! -f "$d/virt-launcher-worker-a-11111.read-error.log" ]
  assert_file_contains 'failed silently' "$d/virt-launcher-worker-a-11111.log"
  assert_file_lacks_pattern 'see read-error.log' "$d/virt-launcher-worker-a-11111.log"
  rm -rf "$tmp"
}

@test "a warning beside a healthy capture is not filed as a read error" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_stderr="Warning: deprecated flag"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-podwarn

  run_capture

  # Same rule the Pod-list read follows: kubectl writes warnings on stderr
  # with a zero status, so presence of stderr does not make it an error.
  d="$COZY_REPORT_DIR/snapshots/console-podwarn/tenant-serial-console"
  assert_file_contains 'deprecated flag' "$d/virt-launcher-worker-a-11111.READ-WARNINGS.txt"
  [ ! -f "$d/virt-launcher-worker-a-11111.read-error.log" ]
  [ ! -f "$d/POD-STATE.txt" ]
  rm -rf "$tmp"
}

@test "a healthy read leaves no read-error file" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-noerr

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/console-noerr/tenant-serial-console/virt-launcher-worker-a-11111.read-error.log" ]
  rm -rf "$tmp"
}

@test "a truncated read is not reported as silence" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-truncated

  out=$( ( set +x; cozy_capture_tenant_serial_console 'a unit test' 6 ) 2>/dev/null )
  printf '%s\n' "$out" >"$tmp/out"

  # The read broke off mid-stream, so the file holds a prefix. Calling that
  # "no console output" is the same unestablished claim about silence the
  # Pod-state capture exists to prevent.
  f="$COZY_REPORT_DIR/snapshots/console-truncated/tenant-serial-console/virt-launcher-worker-a-11111.log"
  assert_file_contains 'mock console output' "$f"
  assert_file_contains 'truncated' "$f"
  assert_file_lacks_pattern 'no console output' "$f"
  # And the summary line the passing path reads must not undo that. It is fed
  # by the same counter that gates the Pod-state read, which a cut-short console
  # needs as much as an empty one -- so counting them together is right and
  # naming them together is not. On the green path this line is the only thing
  # anyone sees, and a partial baseline reported as no baseline sends the reader
  # past evidence that is sitting in the artifact.
  assert_file_contains 'of the 1 guest consoles read, 0 came back empty and 1 were cut short' "$tmp/out"
  rm -rf "$tmp"
}

@test "a failed read records the Pod state too" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-failed-read

  run_capture

  assert_file_contains 'describe pod' "$COZY_REPORT_DIR/snapshots/console-failed-read/tenant-serial-console/POD-STATE.txt"
  rm -rf "$tmp"
}

@test "a healthy capture leaves no Pod-state file" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-healthy

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/console-healthy/tenant-serial-console/POD-STATE.txt" ]
  rm -rf "$tmp"
}

@test "the Pod-state reads are bounded and taken once for the whole selector" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="p1 p2 p3"
  kubectl_logs_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-once

  run_capture

  # One describe and one events read for the selector, not per Pod: three
  # silent workers is one finding, and paying for it per Pod would put an
  # unbounded term back into the failure path the cap exists to bound.
  [ "$(grep -c 'describe pods' "$timeout_calls")" -eq 1 ]
  [ "$(grep -c 'get events' "$timeout_calls")" -eq 1 ]
  rm -rf "$tmp"
}

@test "the attach check accepts the container wherever KubeVirt puts it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  # It is an init container today. The question the check asks is whether the
  # container exists at all, so reading both lists costs nothing and removes
  # the one way a KubeVirt-side move would red a green run over a diagnostic.
  kubectl_init_names="virt-launcher-worker-a-11111= guest-console-log"
  accept_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || accept_rc=$?
  [ "$accept_rc" -eq 0 ]

  kubectl_init_names="virt-launcher-worker-a-11111="
  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?
  [ "$attach_rc" -eq 2 ]
  assert_file_contains '{.spec.containers[*].name}' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a passing attach check records the positive outcome, not just the negative" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="$(printf 'p1=\np2=guest-console-log')"

  ( set +x; cozy_assert_guest_console_attached ) >"$tmp/out" 2>&1

  # This run doubles as the revalidation of a platform-wide workaround, so the
  # positive outcome has to leave an artifact too. Returning 0 in silence
  # leaves "the override worked" to be inferred from the absence of a
  # complaint -- the exact inference this collector refuses to make elsewhere.
  assert_file_contains 'attached on 1 of 2 virt-launcher Pods' "$tmp/out"
  rm -rf "$tmp"
}

@test "a truncated read names the error file when there is one" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_stderr="Warning: stream reset"
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-trunc-err

  run_capture

  # Same discipline as the silent branch: name a file only when it is there.
  d="$COZY_REPORT_DIR/snapshots/console-trunc-err/tenant-serial-console"
  assert_file_contains 'truncated' "$d/virt-launcher-worker-a-11111.log"
  assert_file_contains 'read-error.log' "$d/virt-launcher-worker-a-11111.log"
  assert_file_contains 'stream reset' "$d/virt-launcher-worker-a-11111.read-error.log"
  rm -rf "$tmp"
}

@test "a truncated read with no error file does not name one" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  timeout_fail_pod=virt-launcher-worker-a-11111
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-trunc-noerr

  run_capture

  d="$COZY_REPORT_DIR/snapshots/console-trunc-noerr/tenant-serial-console"
  assert_file_contains 'truncated' "$d/virt-launcher-worker-a-11111.log"
  assert_file_lacks_pattern 'read-error.log' "$d/virt-launcher-worker-a-11111.log"
  rm -rf "$tmp"
}

@test "the typical-cost figure in both chainsaw comments counts the Pod-state reads" {
  # A comment carrying a number that the code contradicts is worse than no
  # number: it outlives the session and is trusted by the next reader.
  for f in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
           hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
    grep -q '4m05s' "$f" || {
      echo "expected $f to state the minReplicas-2 cost including the wedge and Pod-state reads" >&2
      return 1
    }
    grep -q '6m25s' "$f" || {
      echo "expected $f to state the worst-case cost including the wedge read" >&2
      return 1
    }
  done
}

@test "the attach read gives every matched Pod a non-empty line" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111=guest-console-log"

  ( set +x; cozy_assert_guest_console_attached )

  # Asserted on the query rather than the answer, because no mock can stand in
  # for kubectl's jsonpath engine and this is the property the answer depends
  # on. Without the Pod name, a Pod whose initContainers list is absent emits a
  # bare newline that command substitution strips, so "Pods matched, none
  # carries the container" arrives byte-identical to "no Pod matched" -- and
  # the check silently stops being able to report the one thing it is for.
  assert_file_contains '{.metadata.name}{"="}{.spec.initContainers[*].name}' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the failure-path diagnostics carry a client deadline too" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111="

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?

  [ "$attach_rc" -eq 2 ]
  # timeout bounds the process; --request-timeout bounds the call the process
  # makes. The wrapper alone leaves kubectl free to sit in its own retry loop
  # for the whole 30s and return nothing.
  [ "$(grep -c -- '--request-timeout=30s' "$kubectl_calls")" -eq 3 ]
  rm -rf "$tmp"
}

@test "one Pod carrying the container is enough to pass" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="$(printf 'virt-launcher-worker-a-11111=\nvirt-launcher-worker-b-22222=guest-console-log')"

  attach_rc=0
  ( set +x; cozy_assert_guest_console_attached ) || attach_rc=$?

  # Deliberately "any", not "every": the namespace is not scoped to this
  # release, so an every-Pod rule would fail a run over a Pod that is not
  # this test's. It can miss; it cannot false-fail.
  [ "$attach_rc" -eq 0 ]
  rm -rf "$tmp"
}

@test "the attach read is bounded like every other read here" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_init_names="virt-launcher-worker-a-11111=guest-console-log"

  ( set +x; cozy_assert_guest_console_attached )

  assert_file_contains '-k 5 30 kubectl -n tenant-test get pods' "$timeout_calls"
  rm -rf "$tmp"
}

@test "only an answered read that lacks the container fails the suite" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The two non-zero outcomes must not be treated alike: exit 1 says the read
  # did not answer and is not evidence, exit 2 says it did and the container
  # is absent.
  grep -q 'if \[ "${attach_rc}" -eq 2 \]; then' "$lib"
}

@test "the console capture stops at a cap and records what it dropped" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="p1 p2 p3 p4 p5 p6 p7 p8"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-capped

  run_capture

  d="$COZY_REPORT_DIR/snapshots/console-capped/tenant-serial-console"
  [ -f "$d/p6.log" ]
  [ ! -f "$d/p7.log" ]
  assert_file_contains 'stopped after 6 Pods; 8 matched in total' "$d/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "an uncapped pool leaves no truncation marker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="p1 p2"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-uncapped

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/console-uncapped/tenant-serial-console/COLLECTION-TRUNCATED.txt" ]
  rm -rf "$tmp"
}

@test "the attach check sits on the passing path, after the functional block" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Two placements are wrong and this pins against both. In the failure block
  # it would answer only in the run where the console can no longer be
  # recovered. Ahead of the functional assertions it would let a diagnostic
  # preempt the checks the suite exists for.
  gate=$(grep -n '^ *cozy_assert_guest_console_attached || attach_rc=$?$' "$lib" | head -n 1 | cut -d: -f1)
  capture=$(grep -n "^ *cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$gate" ]; then
    echo "expected the suite to verify guest-console-log attached on the passing path" >&2
    return 1
  fi
  if [ -z "$capture" ]; then
    echo "expected to find the failure-path console capture in $lib" >&2
    return 1
  fi
  if [ "$gate" -le "$capture" ]; then
    echo "the attach check (line $gate) must sit after node-join succeeds, not in the failure block (line $capture)" >&2
    return 1
  fi
  # Anchored on the LAST functional assertion in the function, not a
  # mid-file one, or everything between it and the gate is unprotected.
  last=$(grep -n 'helmrelease_has_remediation_cycle "${history_statuses}"' "$lib" | tail -n 1 | cut -d: -f1)
  if [ -z "$last" ]; then
    echo "expected to find the HelmRelease remediation guard in $lib" >&2
    return 1
  fi
  if [ "$gate" -le "$last" ]; then
    echo "the attach check (line $gate) must sit after every functional assertion (last at line $last)" >&2
    return 1
  fi
}

@test "the node-join failure path captures the console before the talosctl capture" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Ordering is the whole point: the talosctl capture needs an apid that the
  # failure class this collects never reached, so running it first spends the
  # failure path's budget on the collector that cannot answer.
  console=$(grep -n "^ *cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  talos=$(grep -n '^ *cozy_capture_tenant_talos "${test_name}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$console" ]; then
    echo "expected the node-join failure path to capture the guest serial console" >&2
    return 1
  fi
  if [ -z "$talos" ]; then
    echo "expected to find the talosctl capture call in $lib" >&2
    return 1
  fi
  if [ "$console" -ge "$talos" ]; then
    echo "serial console capture (line $console) must run before the talosctl capture (line $talos)" >&2
    return 1
  fi
}

@test "the passing path captures the console too, before the tenant is torn down" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # An instrument that only runs on the failure path answers in one direction.
  # Every console this tree has ever captured came from a run that failed, so
  # the boot stages a healthy worker goes through, and how long each takes, have
  # no measured value to compare a failing run against -- and "slower than
  # usual" is the claim the failure keeps resting on. The passing run is where
  # that value comes from, and it exists only until the tenant is deleted.
  gate=$(grep -n '^ *cozy_assert_guest_console_attached || attach_rc=$?$' "$lib" | head -n 1 | cut -d: -f1)
  green=$(grep -n "^ *if ! cozy_capture_tenant_serial_console 'the suite passed" "$lib" | head -n 1 | cut -d: -f1)
  teardown=$(grep -n '^ *kubectl -n tenant-test delete kuberneteses.apps.cozystack.io "${test_name}" --ignore-not-found --wait=false' "$lib" | tail -n 1 | cut -d: -f1)
  for v in gate green teardown; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      return 1
    fi
  done
  # After the attach check, because that check is what says there is a container
  # to read; before the delete, because the Pod holding the stream goes with the
  # tenant and nothing recovers it afterwards.
  if [ "$green" -le "$gate" ] || [ "$green" -ge "$teardown" ]; then
    echo "the passing-path capture (line $green) must sit between the attach check ($gate) and the teardown ($teardown)" >&2
    return 1
  fi
}

@test "the passing path asks for the pool minimum, not the pool" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The failure path runs inside a phase whose budget is derived against the
  # whole operation, so it can afford the pool at its maximum. The passing path
  # has neither a phase nor a budget: it runs inside the same 67m operation the
  # suite has already spent most of, and every read it issues is bounded
  # individually but not as a group. A walk over the maximum pool could
  # therefore turn a suite that proved everything it exists to prove into a red
  # run for collecting a debugging aid slowly. Two workers booting is the same
  # baseline as ten, so that is what it asks for.
  green=$(grep -n "cozy_capture_tenant_serial_console 'the suite passed[^']*' 2;" "$lib" | head -n 1 | cut -d: -f1)
  red=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed[^']*' 6 || true" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$green" ]; then
    echo "expected the passing path to cap its console walk at the pool minimum" >&2
    return 1
  fi
  if [ -z "$red" ]; then
    echo "expected the failure path to keep covering the pool at its maximum" >&2
    return 1
  fi
}

@test "the walk covers exactly the number of Pods the caller asked for" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a virt-launcher-b virt-launcher-c"
  kubectl_logs_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-cap-arg

  out=$( ( set +x; cozy_capture_tenant_serial_console 'a unit test' 2 ) 2>/dev/null )
  printf '%s\n' "$out" >"$tmp/out"

  # The cap is the caller's, not a literal in the walk, or the passing path
  # cannot be cheaper than the failure path and the argument above is decorative.
  dir="$COZY_REPORT_DIR/snapshots/console-cap-arg/tenant-serial-console"
  [ -f "$dir/virt-launcher-a.log" ]
  [ -f "$dir/virt-launcher-b.log" ]
  [ ! -f "$dir/virt-launcher-c.log" ]
  assert_file_contains 'capture stopped after 2 Pods; 3 matched in total' \
    "$dir/COLLECTION-TRUNCATED.txt"
  # And the count on stdout is over the Pods actually read. The cap probe runs
  # on the Pod past the cap, so the counter driving it is one higher than the
  # walk; reported as the denominator it would name a number that is neither
  # what was read nor what matched, and disagree with the truncation marker
  # written beside it -- on the path where nobody opens that marker.
  assert_file_contains 'of the 2 guest consoles read, 2 came back empty and 0 were cut short' "$tmp/out"
  rm -rf "$tmp"
}

@test "a passing-path capture that failed is said out loud instead of failing the suite" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Two failure modes, and this pins against both. Left bare, a non-zero return
  # would fail a suite that proved everything it exists to prove, on a
  # debugging aid. Silenced with `|| true`, a capture that collected nothing
  # would leave a green run whose only trace of the gap is an artifact nobody
  # opens on a green run.
  green=$(grep -n "^ *if ! cozy_capture_tenant_serial_console 'the suite passed" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$green" ]; then
    echo "expected the passing path to capture the console through a tested call" >&2
    return 1
  fi
  # Bounded by the branch rather than by a line count: the warning is what the
  # branch is for, and a fixed offset would break on a comment being added to it
  # while a whole-file search would accept a warning belonging to something
  # else.
  warn=$(awk -v g="$green" '
    NR <= g { next }
    /^  fi$/ { exit }
    /WARNING: the guest console capture/ { print NR; exit }
  ' "$lib")
  if [ -z "$warn" ]; then
    echo "expected the failed passing-path capture to warn in the job log" >&2
    return 1
  fi
}

@test "the capture says which run produced it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-context

  run_capture 'the suite passed: captured after the last assertion'

  # Both paths write the same directory, and the two readings mean opposite
  # things: one is the failure being studied, the other is the baseline it is
  # compared against. Which one a tarball holds is otherwise recoverable only
  # from the run's verdict somewhere else entirely.
  assert_file_contains 'the suite passed: captured after the last assertion' \
    "$COZY_REPORT_DIR/snapshots/console-context/tenant-serial-console/CAPTURE-CONTEXT.txt"
  rm -rf "$tmp"
}

@test "a walk that came back with empty consoles says so on stdout" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_logs_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-silent-count

  out=$( ( set +x; cozy_capture_tenant_serial_console 'a unit test' 6 ) 2>/dev/null )
  printf '%s\n' "$out" >"$tmp/out"

  # The per-pod notes and POD-STATE.txt already record this in the report, and
  # on the failure path that is enough because the report gets opened. The
  # passing path is the one that does not: a baseline that came back empty
  # there would otherwise be visible only to whoever downloads a green run's
  # artifact. The count is not a failure and the capture still returns zero, so
  # stdout is the only place the two callers share.
  assert_file_contains 'of the 1 guest consoles read, 1 came back empty and 0 were cut short' "$tmp/out"
  rm -rf "$tmp"
}

@test "a healthy walk says nothing about silent consoles" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-silent-none

  out=$( ( set +x; cozy_capture_tenant_serial_console 'a unit test' 6 ) 2>/dev/null )
  printf '%s\n' "$out" >"$tmp/out"

  # The other half of the pair. A line printed on every run is a line nobody
  # reads, and this one is meant to be read on the green path.
  assert_file_lacks_pattern 'guest consoles read, ' "$tmp/out"
  rm -rf "$tmp"
}

@test "the context is written even when there is nothing to capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-context-empty
  rc=0

  run_capture 'the suite passed: captured after the last assertion' || rc=$?

  # The empty case is the one where the context matters most: a directory
  # holding only a failure note is where a reader most needs to know which run
  # left it, and the early return is the path most likely to skip writing it.
  [ "$rc" -ne 0 ]
  assert_file_contains 'the suite passed' \
    "$COZY_REPORT_DIR/snapshots/console-context-empty/tenant-serial-console/CAPTURE-CONTEXT.txt"
  rm -rf "$tmp"
}

@test "each console says when it was read, so its last kernel stamp has a wall clock to sit against" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-read-instant

  run_capture

  # A console log is stamped in guest seconds since the guest's own kernel
  # started, and it ends wherever the guest had got to when the read ran. Those
  # two facts alone cannot say whether a short log means a guest that started
  # late or a guest that went quiet, because the instant of the read is not in
  # the file. Without this line the only trace of it is the artifact's mtime,
  # which is metadata rather than content: nothing in the report says it, and a
  # reader has to know to go looking outside the file for it.
  f="$COZY_REPORT_DIR/snapshots/console-read-instant/tenant-serial-console/virt-launcher-worker-a-11111.log"
  assert_file_contains 'console read started at ' "$f"
  assert_file_contains ' epoch seconds' "$f"
  rm -rf "$tmp"
}

@test "the capture records when each guest started, beside the console it captured" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline

  run_capture

  # The other end of the subtraction. With the read instant above and the
  # compute container's start here, how long the guest had been alive when the
  # console was read is one subtraction away, and comparing that against the
  # last stamp in the log says which of the two happened: a guest that had not
  # lived long enough to reach a milestone, or one that lived and stopped
  # printing. Without it both look like the same short file.
  f="$COZY_REPORT_DIR/snapshots/console-timeline/tenant-serial-console/CAPTURE-TIMELINE.txt"
  assert_file_contains 'computeStartedAt=2026-08-15T14:35:47Z' "$f"
  # The subtraction is spelled out rather than left to be reconstructed. This
  # file is read by whoever opens a failed run's tarball months later, and the
  # relation between a guest-relative kernel stamp and two absolute instants is
  # exactly the step a reader gets wrong.
  assert_file_contains 'epoch seconds' "$f"
  rm -rf "$tmp"
}

@test "a start-time read that failed is named rather than left out" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  # Non-zero AND nothing written: the read that never got a row across, which is
  # a different fact from the one cut off part way through and takes a different
  # note. Both have to be staged explicitly or the mock decides which arm runs.
  kubectl_timeline_rc=9
  kubectl_timeline_rows=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-failed

  run_capture

  # An absent file reads as a capture that had no reason to write one, which is
  # the silence every collector here is built to refuse. The read failing is a
  # statement about this machine's view of the cluster, not about when the
  # guests started, and the file has to say which of those it is holding.
  f="$COZY_REPORT_DIR/snapshots/console-timeline-failed/tenant-serial-console/CAPTURE-TIMELINE.txt"
  assert_file_contains 'exit 9' "$f"
  assert_file_lacks_pattern 'computeStartedAt=2026' "$f"
  rm -rf "$tmp"
}

@test "a start-time read cut off part way is not reported as having observed nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_timeline_rc=124
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-cut

  run_capture

  # A read killed at its bound can leave rows behind and still report failure.
  # Saying "when these guests started was not observed" under start times that
  # are plainly sitting there makes the file contradict itself, and a reader who
  # believes the note discards the rows -- which for a capture that exists to be
  # subtracted is the whole value of the file.
  f="$COZY_REPORT_DIR/snapshots/console-timeline-cut/tenant-serial-console/CAPTURE-TIMELINE.txt"
  assert_file_contains 'computeStartedAt=2026-08-15T14:35:47Z' "$f"
  assert_file_contains 'cut off (exit 124)' "$f"
  assert_file_lacks_pattern 'was not observed, which is not the same' "$f"
  rm -rf "$tmp"
}

@test "a start-time read that answered with nothing is not reported as a start time" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_timeline_rows=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-empty

  run_capture

  # The third outcome, and the one that looks most like success: the read
  # answered, and answered with nothing. A zero-length file here would be read
  # as a capture that ran and found the guests had no start times, which is not
  # a state a Pod can be in -- the walk above just read consoles from these
  # same Pods.
  f="$COZY_REPORT_DIR/snapshots/console-timeline-empty/tenant-serial-console/CAPTURE-TIMELINE.txt"
  assert_file_contains 'returned no rows' "$f"
  rm -rf "$tmp"
}

@test "the start-time read is bounded like every other read in this capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-bound

  run_capture

  # This read runs on the same failure path as the rest, where the apiserver is
  # the component least likely to answer. An unbounded one does not lose only
  # itself: it holds the op until the op is killed, and the tenant snapshot
  # queued behind it is lost rather than truncated.
  assert_file_contains 'computeStartedAt' "$timeout_calls"
  assert_file_contains 'request-timeout' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a warning beside a healthy start-time read is not filed as an error" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_timeline_stderr='Warning: v1 Pod is deprecated in this build'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-warn

  run_capture

  # kubectl writes deprecation and partial-result warnings on stderr with a zero
  # exit, so what a message means is decided by the status and not by the fact
  # that something was written. Filed as an error, a healthy read sends the next
  # reader after a failure that did not happen; folded into the rows, it sits
  # inside the data the success probe reads.
  dir="$COZY_REPORT_DIR/snapshots/console-timeline-warn/tenant-serial-console"
  assert_file_contains 'v1 Pod is deprecated' "$dir/CAPTURE-TIMELINE-warnings.txt"
  [ ! -f "$dir/CAPTURE-TIMELINE-error.log" ]
  assert_file_lacks_pattern 'deprecated' "$dir/CAPTURE-TIMELINE.txt"
  rm -rf "$tmp"
}

@test "the stderr of a start-time read that failed is kept as the diagnosis" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_timeline_rc=1
  kubectl_timeline_rows=
  kubectl_timeline_stderr='error: the server could not find the requested resource'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-err

  run_capture

  # The other direction: on a failed read that message IS the diagnosis, and
  # dropping it leaves a note saying the read failed with nothing saying why.
  # The note has to name the file, or the evidence sits in a directory the
  # reader has no reason to open.
  dir="$COZY_REPORT_DIR/snapshots/console-timeline-err/tenant-serial-console"
  assert_file_contains 'could not find the requested resource' "$dir/CAPTURE-TIMELINE-error.log"
  [ ! -f "$dir/CAPTURE-TIMELINE-warnings.txt" ]
  assert_file_contains 'CAPTURE-TIMELINE-error.log' "$dir/CAPTURE-TIMELINE.txt"
  rm -rf "$tmp"
}

@test "a start-time read that failed without a word does not point at a missing file" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  kubectl_timeline_rc=124
  kubectl_timeline_rows=
  kubectl_timeline_stderr=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-silent

  run_capture

  # timeout kills the child without a word, so this read can fail having written
  # to neither stream. Naming the error log unconditionally would then send the
  # reader after a file that was never created, which is the same trap the
  # per-Pod notes above are written to avoid.
  dir="$COZY_REPORT_DIR/snapshots/console-timeline-silent/tenant-serial-console"
  [ ! -f "$dir/CAPTURE-TIMELINE-error.log" ]
  assert_file_contains 'gave no reason on either stream' "$dir/CAPTURE-TIMELINE.txt"
  assert_file_lacks_pattern 'reason it gave is in' "$dir/CAPTURE-TIMELINE.txt"
  rm -rf "$tmp"
}

@test "the console container's start is asked for in both status lists" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-worker-a-11111"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=console-timeline-lists

  run_capture

  # KubeVirt runs guest-console-log as an init container, so its runtime state
  # lives under initContainerStatuses. Asking only containerStatuses returns
  # empty for a container that started perfectly well, and an empty start time
  # here is supposed to mean the container never ran -- the one reading this
  # field exists to support. The attach check reads both spec lists for the
  # same reason; a missing key costs an empty string, so naming both is free.
  assert_file_contains 'initContainerStatuses[?(@.name=="guest-console-log")]' "$kubectl_calls"
  assert_file_contains 'containerStatuses[?(@.name=="compute")]' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a run near its operation ceiling declines the baseline instead of being killed collecting it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # The capture costs real wall clock and runs after everything the suite
  # proves. Inside an operation with a fixed ceiling that makes it a way for a
  # green run to end red, which is the one thing a passing-path collector may
  # not do. Driven rather than grepped: the direction of the comparison IS the
  # safety property, and an inverted one runs the capture on exactly the runs
  # that cannot afford it while every structural check still passes.
  date_now=1000000000
  _COZY_RUN_STARTED_AT=$(( date_now - (COZY_OP_CEILING - COZY_GREEN_CAPTURE_RESERVE) - 1 ))
  rc=0
  ( set +x; cozy_green_capture_has_room 'the baseline' ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 1 ]
  # And the decline is spoken. A collector that stops running silently leaves a
  # green run that looks exactly like one where the baseline was collected and
  # came back empty, which is the reading every note in this file exists to
  # prevent.
  assert_file_contains 'not collected' "$tmp/out"
  assert_file_contains 'the baseline' "$tmp/out"
  rm -rf "$tmp"
}

@test "a run with the operation's room to spare collects the baseline" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # The other direction, and the one an inverted comparison breaks silently: a
  # gate that declines a run with forty minutes left costs the baseline on every
  # healthy run, and the only symptom is a diagnostic that stopped appearing.
  date_now=1000000000
  _COZY_RUN_STARTED_AT=$(( date_now - 60 ))
  rc=0
  ( set +x; cozy_green_capture_has_room 'the baseline' ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_lacks_pattern 'not collected' "$tmp/out"
  rm -rf "$tmp"
}

@test "a run whose start was never stamped declines rather than dying on the arithmetic" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # The stamp is set by run_kubernetes_test, which no unit test invokes, so
  # nothing else here would notice it being deleted. Under the `set -eu` the
  # chainsaw script runs with, an unbound name inside the arithmetic aborts the
  # whole function -- ending a passing run red, which is the outcome this gate
  # exists to prevent. Declared zero at file scope, it fails closed instead.
  date_now=1000000000
  rc=0
  ( set -u; set +x; cozy_green_capture_has_room 'the baseline' ) >"$tmp/out" 2>&1 || rc=$?

  [ "$rc" -eq 1 ]
  assert_file_contains 'not collected' "$tmp/out"
  rm -rf "$tmp"
}

@test "the passing path decides through the gate rather than around it" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The behavioural tests above cover the predicate. Its wiring is what they
  # cannot reach: run_kubernetes_test is not callable from a unit test, so the
  # stamp it takes and the gate it calls are only assertable as source. Both
  # halves fail the same silent way -- a correct predicate nothing calls
  # protects nothing, and a predicate called without the stamp reads the whole
  # epoch as elapsed and declines the baseline on every run forever.
  stamp=$(grep -n '^  _COZY_RUN_STARTED_AT=\$(date +%s)$' "$lib" | head -n 1 | cut -d: -f1)
  gate=$(grep -n 'cozy_green_capture_has_room ' "$lib" | tail -n 1 | cut -d: -f1)
  green=$(grep -n "cozy_capture_tenant_serial_console 'the suite passed" "$lib" | head -n 1 | cut -d: -f1)
  for v in stamp gate green; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      return 1
    fi
  done
  # Inside the function whose elapsed time it measures, not at file scope: taken
  # once when the library is sourced, it would time the sourcing rather than the
  # run, and every suite after the first would read as already over its ceiling.
  fn=$(grep -n '^run_kubernetes_test() {$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$fn" ] || [ "$stamp" -le "$fn" ]; then
    echo "the run's start stamp (line $stamp) must be taken inside run_kubernetes_test (line $fn)" >&2
    return 1
  fi
  if [ "$stamp" -ge "$gate" ] || [ "$gate" -ge "$green" ]; then
    echo "expected the stamp ($stamp) before the gate ($gate) before the capture ($green)" >&2
    return 1
  fi
}

@test "the operation ceiling the reserve is measured against is the one the suites give" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # A reserve is only meaningful against the right ceiling. This number is a
  # restatement of one that lives in the suite files, so it is read from both
  # sides rather than trusted: a suite that raises or lowers its `timeout:`
  # otherwise leaves the reserve protecting a ceiling that moved, and the
  # decline either stops firing when it should or starts firing when it should
  # not.
  ceiling=$(grep -oE '^COZY_OP_CEILING=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  reserve=$(grep -oE '^COZY_GREEN_CAPTURE_RESERVE=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  for v in ceiling reserve; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  for f in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
           hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
    # The first `timeout:` in each file is the bringup op's, which is the one
    # this whole helper runs inside; the teardown step below it has its own.
    minutes=$(grep -oE '^ *timeout: [0-9]+m$' "$f" | head -n 1 | grep -oE '[0-9]+')
    if [ -z "$minutes" ]; then
      echo "expected to read the operation timeout from $f" >&2
      return 1
    fi
    if [ "$((minutes * 60))" -ne "$ceiling" ]; then
      echo "$f gives the operation ${minutes}m but the reserve is measured against ${ceiling}s" >&2
      return 1
    fi
  done
  # And the reserve has to leave room for the capture it is reserving for, or it
  # protects nothing: the walk it admits would still run past the ceiling.
  if [ "$reserve" -lt 210 ]; then
    echo "the reserve ${reserve}s is under the 210s the capture can cost at this caller's cap" >&2
    return 1
  fi
}

@test "the e2e tenant node group asks KubeVirt for the guest console log" {
  # Anchored to a bare six-space-indented key so a comment or a prose mention
  # of the field cannot stand in for the setting itself. It does not prove the
  # key sits in the nodeGroups block specifically -- the render is what would
  # catch that, and the chart's own suites cover it.
  grep -q '^      logSerialConsole: true$' hack/e2e-chainsaw/_lib/run-kubernetes.sh
}
