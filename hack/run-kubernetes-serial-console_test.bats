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
talosctl_calls=/dev/null
timeout_calls=/dev/null
timeout_fail_pod=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"

  if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then
    case "$*" in
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
  ( set +x; cozy_capture_tenant_serial_console ) || _rc=$?
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
  # "fewer than 2 tenant nodes Ready within 18m" -- byte-identical to the
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

  run_capture

  # The read broke off mid-stream, so the file holds a prefix. Calling that
  # "no console output" is the same unestablished claim about silence the
  # Pod-state capture exists to prevent.
  f="$COZY_REPORT_DIR/snapshots/console-truncated/tenant-serial-console/virt-launcher-worker-a-11111.log"
  assert_file_contains 'mock console output' "$f"
  assert_file_contains 'truncated' "$f"
  assert_file_lacks_pattern 'no console output' "$f"
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
    grep -q '3m30s' "$f" || {
      echo "expected $f to state the minReplicas-2 cost including the wedge and Pod-state reads" >&2
      return 1
    }
    grep -q '5m50s' "$f" || {
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
  capture=$(grep -n '^ *cozy_capture_tenant_serial_console || true$' "$lib" | head -n 1 | cut -d: -f1)
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
  console=$(grep -n '^ *cozy_capture_tenant_serial_console || true$' "$lib" | head -n 1 | cut -d: -f1)
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

@test "the e2e tenant node group asks KubeVirt for the guest console log" {
  # Anchored to a bare six-space-indented key so a comment or a prose mention
  # of the field cannot stand in for the setting itself. It does not prove the
  # key sits in the nodeGroups block specifically -- the render is what would
  # catch that, and the chart's own suites cover it.
  grep -q '^      logSerialConsole: true$' hack/e2e-chainsaw/_lib/run-kubernetes.sh
}
