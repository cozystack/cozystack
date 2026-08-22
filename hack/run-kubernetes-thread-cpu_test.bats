#!/usr/bin/env bats
# Regression coverage for the tenant-worker per-thread CPU capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why the collector beside it cannot: the
# throttling capture reads the compute container's cgroup, and that cgroup holds
# every thread QEMU runs -- the guest's vCPUs, the emulator thread, and the IO
# helpers. A container burning most of its quota while the guest reports no
# progress is therefore two different findings wearing the same number, and the
# container-level counter cannot separate them. Only the per-thread split can,
# and it exists nowhere else in this tree. These tests pin the split and the
# separation between an unread container and a quiet one, not the reading.

kubectl_calls=/dev/null
kubectl_pod_names=
kubectl_list_rc=0
kubectl_list_stderr=
kubectl_exec_rc=0
kubectl_exec_stderr=
# Two stat lines from one process, in the shape /proc writes them. The second
# is the one every field-counting reader gets wrong: a KVM vCPU thread is named
# "CPU 0/KVM", the space sits inside the parentheses, and every field after it
# shifts by one.
kubectl_exec_output='clock ticks per second: 100
process 41 comm qemu-kvm
41 (qemu-kvm) S 1 41 41 0 -1 4194560 8100 0 0 0 4210 1180 0 0 20 0 9 0 5512
57 (CPU 0/KVM) R 1 41 41 0 -1 4194368 120 0 0 0 39120 2210 0 0 20 0 9 0 5530'
timeout_calls=/dev/null
timeout_fail_pod=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"

  if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then
    [ -z "${kubectl_list_stderr}" ] || printf '%s\n' "${kubectl_list_stderr}" >&2
    [ "${kubectl_list_rc}" -eq 0 ] || return "${kubectl_list_rc}"
    [ -z "${kubectl_pod_names}" ] || printf '%s\n' ${kubectl_pod_names}
    return 0
  fi

  if [ "${3:-}" = exec ]; then
    [ -z "${kubectl_exec_stderr}" ] || printf '%s\n' "${kubectl_exec_stderr}" >&2
    # Output before the status, so the branch that labels a partial capture is
    # reachable: a read cut off part way through has already written what it
    # managed to read, and a stub that returns first can only ever stage
    # empty-plus-failure.
    [ -z "${kubectl_exec_output}" ] || printf '%s\n' "${kubectl_exec_output}"
    return "${kubectl_exec_rc}"
  fi

  return 0
}

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # Return 97 rather than running the command when the wrapper is not the
  # bounded form: a read that lost its ceiling must not pass as a read.
  [ "${1:-}" = -k ] || return 97
  shift 3
  "$@" || command_rc=$?
  if [ -n "${timeout_fail_pod}" ]; then
    case " $* " in
      *"${timeout_fail_pod}"*) return 124 ;;
    esac
  fi
  return "${command_rc}"
}

assert_file_contains() {
  local needle="$1"
  local file="$2"

  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${needle}" >&2
    return 1
  fi
  case "$(cat "${file}")" in
    *"${needle}"*) return 0 ;;
  esac
  printf 'expected %s to contain: %s\n' "${file}" "${needle}" >&2
  return 1
}

assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"

  # A missing file must fail rather than vacuously pass: a bare `! grep -q`
  # succeeds on an unreadable path, which is indistinguishable from "the file
  # exists and does not carry the label".
  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Branched on awk's exact status. awk exits 1 for "no line matched" and 2 for
  # "I could not evaluate this"; folded into an `if`, both land in the passing
  # branch and the assertion "this pattern is absent" is satisfied by the
  # matcher giving up. Every claim in this file about something NOT appearing
  # rests on the three-way split below.
  local _rc=0
  awk -v pattern="${pattern}" '$0 ~ pattern { found = 1 } END { exit found ? 0 : 1 }' "${file}" || _rc=$?
  case "${_rc}" in
    0)
      printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
      return 1
      ;;
    1) return 0 ;;
    *)
      printf 'awk could not evaluate pattern %s against %s\n' "${pattern}" "${file}" >&2
      return 1
      ;;
  esac
}

# The function under test redirects a command's stderr into a report file and
# decides which artifact to write from it. cozytest.sh runs under `set -x`, so
# the tracer's own line for that command lands on the very stderr being
# redirected, and every assertion about those files would be reading the
# tracer. Call through this wrapper so the sinks hold what production writes.
run_capture() {
  local _rc=0
  ( set +x; cozy_capture_tenant_worker_thread_cpu 1 ) || _rc=$?
  return "${_rc}"
}

@test "a negative assertion fails when its own matcher cannot evaluate" {
  # The helper below every "must not appear" claim in this file. What rides on
  # it here is the collector's contract stated negatively -- that an unreachable
  # container is not written up as one that answered, that an incomplete read
  # carries no pairing instruction -- so a matcher that fails open turns each of
  # them into a sentence nobody checked.
  tmp=$(mktemp -d)
  printf 'anything\n' >"$tmp/subject"

  if assert_file_lacks_pattern '*(' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: an unparseable pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  assert_file_lacks_pattern 'nothinglikethis' "$tmp/subject"
  if assert_file_lacks_pattern 'anything' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: a present pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "the threads are read from inside the compute container of each worker Pod" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  # The container has to be named. virt-launcher runs several, and kubectl's
  # default is the first one in the spec rather than the one running QEMU, so an
  # unnamed exec reads a stranger's threads or fails on a container with no shell.
  assert_file_contains 'exec virt-launcher-a -c compute' "$kubectl_calls"
  # The projection is a parameter now, and the two callers of that listing pass
  # different ones from twelve hundred lines apart. Swapped, this collector
  # would exec into Pods named after nodes and the cAdvisor one would ask
  # kubelets named after Pods: both dead, neither with an error, and every
  # artifact full of sentences saying the subject was not reached. Nothing else
  # in either suite sees the argument, because the mocks answer any listing
  # regardless of what was asked for.
  assert_file_contains 'jsonpath={range .items[?(@.status.phase=="Running")]}{.metadata.name}' "$kubectl_calls"
  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # The vCPU line and the emulator line both, because the whole question is
  # which of them carries the time: a capture that kept only one of the two
  # would answer it by construction.
  assert_file_contains '(CPU 0/KVM)' "$capture"
  assert_file_contains '(qemu-kvm)' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unknown' "$capture"
  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  [ ! -f "$dir/virt-launcher-a.exec-error.log" ]
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  rm -rf "$tmp"
}

@test "each read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  # An unbounded exec would hold the whole diagnostics block against the wedged
  # kubelet this collector exists to study, and the phase budget bounds when a
  # read may START, never how long one already running may take.
  # Named by their verbs: both reads carry the same bound, so a bare '-k 5 20'
  # would be satisfied twice by whichever one survived.
  assert_file_contains '-k 5 20 kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher' "$timeout_calls"
  assert_file_contains '-k 5 20 kubectl -n tenant-test exec' "$timeout_calls"
  # The listing is a request and carries the second bound too; the exec is a
  # streamed connection and the tree's other exec captures bound it with the
  # wrapper alone.
  assert_file_contains '--request-timeout=20s' "$kubectl_calls"
  assert_file_contains '[capture exit code: 0]' \
    "$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  rm -rf "$tmp"
}

@test "the sample number decides which reading a file belongs to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  ( set +x; cozy_capture_tenant_worker_thread_cpu 2 )

  # utime and stime are cumulative since the thread started, so one reading is
  # an average over an uptime and says nothing about which thread burned the
  # window the deadline covered. A second reading written over the first leaves
  # one file and no interval.
  assert_file_contains '(CPU 0/KVM)' \
    "$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-2/virt-launcher-a.txt"
  [ ! -d "$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1" ]
  rm -rf "$tmp"
}

@test "each reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # The wait between the two passes is not the interval this subject's two
  # readings span: the other two subjects' passes fall between them, and on the
  # run this collector exists for those passes are the slow part. The stamps are
  # what make the real interval readable off the pair.
  assert_file_contains 'read attempted from' "$capture"
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the legend names the field positions and the thread name that shifts them" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # Raw lines with no legend are a trap rather than data. The two numbers a
  # reader wants sit at fixed positions in /proc/<pid>/task/<tid>/stat, and the
  # thread this capture exists to identify is the one whose name breaks the
  # count: awk over whitespace reads the wrong column for exactly the vCPU
  # threads and the right one for every other thread, which is the shape of a
  # wrong answer that looks right.
  assert_file_contains 'utime' "$capture"
  assert_file_contains 'stime' "$capture"
  assert_file_contains 'CPU 0/KVM' "$capture"
  # The escape from the trap, not just a warning about it. A legend that names
  # the hazard without naming the parse leaves the next reader to invent one.
  assert_file_contains 'last )' "$capture"
  rm -rf "$tmp"
}

@test "the legend names which thread each comm belongs to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # The whole discriminator is which of four kinds of thread holds the time, so
  # a capture that does not say which name is which kind leaves the reader to
  # guess the answer it was collected to settle.
  assert_file_contains 'emulator' "$capture"
  rm -rf "$tmp"
}

@test "the capture tells the reader how to pair it with its sibling sample" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  assert_file_contains 'subtract this Pod file from its sibling under the other sample directory' "$capture"
  rm -rf "$tmp"
}

@test "a read that did not finish is not told to pair with a sibling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  # Lines arrived and the read still failed, which is a stream cut off part way
  # through the thread list.
  kubectl_exec_rc=124

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  assert_file_contains 'these thread counters are incomplete' "$capture"
  # Telling a reader to subtract two files asserts that both hold a whole
  # reading. A difference taken against a truncated one understates whichever
  # threads the read did not reach, and the threads it did not reach are the
  # ones listed last -- so the understated set is not even random.
  assert_file_lacks_pattern 'subtract this Pod file from its sibling' "$capture"
  # And a legend that explains a column layout the file may not carry in full
  # is the same overclaim one line down.
  assert_file_lacks_pattern 'last \)' "$capture"
  rm -rf "$tmp"
}

@test "a compute container that was not reached is recorded as unknown, not as an idle guest" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=1
  kubectl_exec_output=
  kubectl_exec_stderr='Error from server: error dialing backend: remote error: tls: internal error'

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  capture="$dir/virt-launcher-a.txt"
  # An empty file here reads as a guest whose threads were all idle, which is
  # the one conclusion this collector must never manufacture: the container
  # dying is a routine outcome on this failure path and it is not a reading.
  assert_file_contains 'was not reached' "$capture"
  assert_file_contains 'exec-error.log' "$capture"
  assert_file_lacks_pattern 'subtract this Pod file from its sibling' "$capture"
  assert_file_contains 'error dialing backend' "$dir/virt-launcher-a.exec-error.log"
  rm -rf "$tmp"
}

@test "an exec that died without a word is not reported as a container that answered" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  # timeout kills its child without a word and kubectl dies on SIGTERM the same
  # way, so the dominant failure here arrives non-zero with an empty error log.
  # Keyed on stderr rather than on the status, the artifact would say the
  # opposite of what happened.
  kubectl_exec_rc=137
  kubectl_exec_output=
  kubectl_exec_stderr=

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  capture="$dir/virt-launcher-a.txt"
  assert_file_contains 'was not reached' "$capture"
  assert_file_contains 'without a word on either stream' "$capture"
  # Naming a file that was never written sends the reader looking for evidence
  # that does not exist.
  [ ! -f "$dir/virt-launcher-a.exec-error.log" ]
  assert_file_contains '[exit 137:' "$capture"
  rm -rf "$tmp"
}

@test "a container that answered and said nothing is not reported as unreachable" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=0
  kubectl_exec_output=

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # Two different problems that leave the same empty file: an exec that never
  # landed, and a shell that ran and printed nothing. They are fixed by looking
  # in different places, so they must not share a sentence.
  assert_file_contains 'answered and returned nothing' "$capture"
  assert_file_lacks_pattern 'was not reached' "$capture"
  rm -rf "$tmp"
}

@test "a container whose QEMU is gone carries no legend for columns it does not have" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=0
  kubectl_exec_output='clock ticks per second: 100
NO-QEMU-PROCESS: no process whose name starts with qemu- exists in this container, so it had no threads to read'

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # A clean read that found nothing to read is a finding about the failure, and
  # a different one from every arm around it.
  assert_file_contains 'nothing to split' "$capture"
  # A legend explaining a column layout over a file with no columns is the same
  # overclaim as a pairing instruction over a partial read, and it would send
  # the reader looking for stat lines that are not there.
  assert_file_lacks_pattern 'last \)' "$capture"
  assert_file_lacks_pattern 'subtract this Pod file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a warning beside a healthy listing is not filed as a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  kubectl_list_stderr='Warning: v1 Pod is deprecated in this cluster'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # kubectl writes deprecation and partial-result warnings with a zero exit, so
  # a sink chosen by "something was written" files a healthy read as a failed
  # one and the capture beside it reads as salvage.
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  assert_file_contains 'deprecated' "$dir/READ-WARNINGS.txt"
  rm -rf "$tmp"
}

@test "a warning beside a healthy exec is not filed as a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=0
  kubectl_exec_stderr='Defaulted container "compute" out of: compute, volumecontainerdisk'

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # kubectl writes deprecation and defaulted-container notices on stderr with a
  # zero exit, so the sink is chosen by the status rather than by something
  # having been written. Filed as an error, a healthy exec would look failed and
  # the capture beside it would read as salvage; discarded, a real notice about
  # the read would be gone. The two sibling collectors pin this branch, and the
  # round that suppressed the probe's own stderr rests on it.
  assert_file_contains 'Defaulted container' "$dir/virt-launcher-a.READ-WARNINGS.txt"
  [ ! -f "$dir/virt-launcher-a.exec-error.log" ]
  assert_file_contains '(CPU 0/KVM)' "$dir/virt-launcher-a.txt"
  rm -rf "$tmp"
}

@test "a Pod list that never returned is not reported as no workers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_list_rc=1
  kubectl_list_stderr='Unable to connect to the server: dial tcp: i/o timeout'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture || true

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # An apiserver that never answered and a namespace with no worker in it are
  # different findings, and an empty directory is how they become one.
  assert_file_contains 'failed to list' "$dir/COLLECTION-FAILED.txt"
  assert_file_contains 'i/o timeout' "$dir/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a namespace with no worker Pod says so instead of leaving an empty tree" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture || true

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # Named for the filter the query carries. A namespace whose launchers are all
  # Pending is a different finding from one with no launcher at all, and the
  # sentence has to be true of the listing that was actually issued.
  assert_file_contains 'no Running virt-launcher Pod' "$dir/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "the walk is capped and names both counts when it stops" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a virt-launcher-b virt-launcher-c virt-launcher-d virt-launcher-e"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # A short listing otherwise reads as a small pool rather than a truncated
  # walk, and the two counts are what separate them. Asserted as the whole
  # sentence, the way the sibling suites assert theirs: the marker's claim is
  # that the two numbers DIFFER, and a pair of bare digit checks is satisfied by
  # a marker reading "stopped after 4 Pods; 4 matched in total".
  assert_file_contains 'capture stopped after 4 Pods; 5 matched in total' \
    "$dir/COLLECTION-TRUNCATED.txt"
  # And the cap's VALUE, not just its wording. The marker prints max_pods rather
  # than what the walk read, so an off-by-one in the boundary test leaves the
  # sentence byte-identical while a Pod goes unread -- and max_pods is the term
  # the node-join budget guard multiplies, so the arithmetic and the marker
  # would misstate the same run together.
  [ -f "$dir/virt-launcher-d.txt" ]
  [ ! -f "$dir/virt-launcher-e.txt" ]
  rm -rf "$tmp"
}

@test "a pool below the cap leaves no truncation marker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a virt-launcher-b"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # A marker on every run is a marker nobody reads, and this one is the only
  # thing that says a capture answered for part of the pool.
  [ ! -f "$dir/COLLECTION-TRUNCATED.txt" ]
  [ -f "$dir/virt-launcher-b.txt" ]
  rm -rf "$tmp"
}

@test "a Pod whose read times out does not stop the next one" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a virt-launcher-b"
  timeout_fail_pod=virt-launcher-a
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1"
  # The two workers of one cluster are the comparison this capture is for, so a
  # walk that abandons the rest after one wedged container answers half the
  # question while looking like it answered all of it.
  assert_file_contains '[exit 124:' "$dir/virt-launcher-a.txt"
  assert_file_contains '(CPU 0/KVM)' "$dir/virt-launcher-b.txt"
  rm -rf "$tmp"
}

@test "the probe finds the qemu process and reads every one of its threads" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # A /proc the size of the one inside a virt-launcher: the QEMU process, a
  # neighbour that is not it, and two threads under QEMU.
  mkdir -p "$tmp/proc/41/task/41" "$tmp/proc/41/task/57" "$tmp/proc/9/task/9"
  printf 'qemu-kvm\n' >"$tmp/proc/41/comm"
  printf 'virt-launcher\n' >"$tmp/proc/9/comm"
  printf '41 (qemu-kvm) S 1 41 41 0 -1 4194560 8100 0 0 0 4210 1180\n' >"$tmp/proc/41/task/41/stat"
  printf '57 (CPU 0/KVM) R 1 41 41 0 -1 4194368 120 0 0 0 39120 2210\n' >"$tmp/proc/41/task/57/stat"
  printf '9 (virt-launcher) S 0 9 9 0 -1 4194304 900 0 0 0 12 4\n' >"$tmp/proc/9/task/9/stat"

  out=$(sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc")

  printf '%s\n' "$out" >"$tmp/out"
  # Both threads of the QEMU process, or the split this capture exists for is
  # taken over a subset chosen by the probe rather than by the guest.
  assert_file_contains '(qemu-kvm)' "$tmp/out"
  assert_file_contains '(CPU 0/KVM)' "$tmp/out"
  # And nothing from the neighbour: virt-launcher's own threads are outside the
  # question and would be read as QEMU's under a heading that says QEMU's.
  assert_file_lacks_pattern 'virt-launcher\) S' "$tmp/out"
  rm -rf "$tmp"
}

@test "a process that vanished mid-walk leaves nothing on the probe's stderr" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  mkdir -p "$tmp/proc/41/task/41" "$tmp/proc/99"
  printf 'qemu-kvm\n' >"$tmp/proc/41/comm"
  printf '41 (qemu-kvm) S 1 41 41 0 -1 0 0 0 0 0 4210 1180\n' >"$tmp/proc/41/task/41/stat"
  # An entry the glob finds and the read cannot open, which is what a thread
  # exiting between the two looks like. A dangling symlink stages it the same
  # way whatever uid the suite runs as, where a chmod would not.
  ln -s /definitely-not-here "$tmp/proc/99/comm"

  sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc" >"$tmp/out" 2>"$tmp/err"

  # The probe's stderr is the collector's error artifact. A shell complaint
  # about a process that had already exited lands there, and on a clean read the
  # collector files it as a read warning -- a healthy capture shipping a warning
  # about something that did not affect the reading, which is the misfiling
  # every branch in this collector is written against.
  if [ -s "$tmp/err" ]; then
    echo "expected the probe to say nothing on stderr; it wrote:" >&2
    cat "$tmp/err" >&2
    return 1
  fi
  # And the suppression did not cost the reading it was protecting.
  assert_file_contains '(qemu-kvm)' "$tmp/out"
  rm -rf "$tmp"
}

@test "the probe says so when no qemu process exists rather than returning nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  mkdir -p "$tmp/proc/9/task/9"
  printf 'virt-launcher\n' >"$tmp/proc/9/comm"
  printf '9 (virt-launcher) S 0 9 9 0 -1 4194304 900 0 0 0 12 4\n' >"$tmp/proc/9/task/9/stat"

  out=$(sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc")

  printf '%s\n' "$out" >"$tmp/out"
  # A container whose QEMU has already exited is a finding about the failure,
  # and silence from the probe would reach the artifact as a container that
  # answered and said nothing -- a different problem, in a different place.
  assert_file_contains 'NO-QEMU-PROCESS' "$tmp/out"
  # And it reports having read the neighbour, because that is what separates
  # this from a probe that could read nothing at all.
  assert_file_lacks_pattern 'NO-PROC-READ' "$tmp/out"
  rm -rf "$tmp"
}

@test "the probe says so when QEMU is named and its threads are already gone" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # The race the probe runs into by construction: the comm read succeeds and
  # the task directory is gone by the time it is opened. Staged as a process
  # with no task tree at all, which is what the walk sees either way.
  mkdir -p "$tmp/proc/41"
  printf 'qemu-kvm\n' >"$tmp/proc/41/comm"

  out=$(sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc")

  printf '%s\n' "$out" >"$tmp/out"
  # The heading alone makes the capture non-empty, so nothing downstream can
  # tell this from a reading unless the probe says it. Reported as its own
  # outcome rather than as "no QEMU": QEMU was there, which is a different
  # sentence about the guest.
  assert_file_contains 'NO-THREAD-LINES' "$tmp/out"
  assert_file_lacks_pattern 'NO-QEMU-PROCESS' "$tmp/out"
  rm -rf "$tmp"
}

@test "a capture carrying only a process heading is not given a column legend" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=0
  kubectl_exec_output='clock ticks per second: 100
process 41 comm qemu-kvm
NO-THREAD-LINES: a qemu-* process was named and not one of its task stat files could be read, so it exited between the two reads'

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  assert_file_contains 'threads were gone before they could be read' "$capture"
  # The whole hazard: a heading is enough to make the file non-empty, so every
  # "was it read" arm above passes and the legend would describe columns this
  # capture does not have.
  assert_file_lacks_pattern 'last \)' "$capture"
  assert_file_lacks_pattern 'subtract this Pod file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "the probe reports a proc it could not read apart from a container without QEMU" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  # A proc mount with nothing readable under it. The two outcomes are fixed in
  # different places, and only one of them is about the guest: reported as the
  # other, the artifact would state that QEMU had exited on the strength of a
  # read that never happened.
  mkdir -p "$tmp/proc"

  out=$(sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc")

  printf '%s\n' "$out" >"$tmp/out"
  assert_file_contains 'NO-PROC-READ' "$tmp/out"
  assert_file_lacks_pattern 'NO-QEMU-PROCESS' "$tmp/out"
  rm -rf "$tmp"
}

@test "a proc the probe could not read is not written up as a guest without QEMU" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_pod_names="virt-launcher-a"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=thread-smoke
  kubectl_exec_rc=0
  kubectl_exec_output='clock ticks per second: 100
NO-PROC-READ: not one process could be read under the proc mount, so this says nothing about what the container was running'

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/thread-smoke/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  # The collector has to carry the distinction the probe drew, or drawing it
  # bought nothing: a statement about the probe filed as a statement about the
  # guest is the conflation this whole collector is built against.
  assert_file_contains 'the thread split is unknown' "$capture"
  assert_file_lacks_pattern 'nothing to split' "$capture"
  assert_file_lacks_pattern 'last \)' "$capture"
  rm -rf "$tmp"
}

@test "the probe reports the tick rate its two numbers are counted in" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  mkdir -p "$tmp/proc/9/task/9"
  printf 'virt-launcher\n' >"$tmp/proc/9/comm"

  out=$(sh -c "$(_cozy_thread_cpu_probe)" probe "$tmp/proc")

  printf '%s\n' "$out" >"$tmp/out"
  # utime and stime are clock ticks, and the figure this capture is read
  # against is cores. Without the rate on the same page the reader supplies one
  # from memory, which is where a factor of ten enters a measurement.
  assert_file_contains 'clock ticks per second' "$tmp/out"
  rm -rf "$tmp"
}

@test "with timeout off PATH the exec still runs instead of exiting 127" {
  # The fallback arm is what the phase-start warning promises for this
  # collector: on a runner without the binary it runs UNBOUNDED rather than
  # exiting 127 and leaving an empty directory. A promise the code does not keep
  # is what this collector's every other branch is against, so the arm is
  # entered here rather than inspected.
  #
  # A subprocess with a stripped PATH, because this file mocks `timeout` as a
  # shell function and `command -v` finds functions -- the condition cannot be
  # reached from inside a test body.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  # Everything the collector shells out to, minus `timeout` itself. Missing one
  # would fail this test for the wrong reason and read as the arm being broken.
  for c in mkdir sort grep mv rm wc tr cat date; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in mkdir sort grep mv rm wc tr cat date; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  # Only the staged directory is checked. `command -v timeout` is always true
  # here -- this file defines it as a shell function, which is the very fact the
  # subprocess exists to escape.
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi

  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl() {
      # To a file, not to stderr. The collector redirects the stderr of every
      # read into its own artifact, so a stub that announces itself there is
      # captured by the code under test and never reaches this output.
      printf "KUBECTL %s\n" "$*" >>"$CALLS"
      if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then printf "virt-launcher-a\n"; return 0; fi
      if [ "${3:-}" = exec ]; then printf "41 (qemu-kvm) S 1 41 41 0 -1 0 0 0 0 0 4210 1180\n"; return 0; fi
      return 0
    }
    CALLS='"$tmp"'/calls
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=fallback
    PATH='"$tmp"'/bin
    cozy_capture_tenant_worker_thread_cpu 1
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/out"

  # The read ran at all -- bounded into exit 127 would leave nothing here.
  assert_file_contains 'KUBECTL -n tenant-test exec virt-launcher-a -c compute' "$tmp/calls"
  # And produced thread lines rather than an "unknown" label.
  assert_file_contains '(qemu-kvm)' \
    "$tmp/report/snapshots/fallback/tenant-thread-cpu/sample-1/virt-launcher-a.txt"
  rm -rf "$tmp"
}

@test "the collector's own caps stay what its comments claim" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The Pod listing lives in the body this collector shares with the cAdvisor
  # captures, so the subject here is the code the collector runs rather than the
  # function that carries its name; reading only the named function would leave
  # the listing assertions vacuously true against a body it does not contain.
  fn=$(awk '/^cozy_capture_tenant_worker_thread_cpu\(\)/,/^}/' "$lib"
      awk '/^_cozy_virt_launcher_listing\(\)/,/^}/' "$lib")
  if [ -z "$fn" ]; then
    echo "expected to find the collector in $lib" >&2
    return 1
  fi
  case "$fn" in
    *'max_pods=4'*) ;;
    *) echo "expected the collector to cap the walk at the 4 Pods the comments state" >&2; return 1 ;;
  esac
  # The knob, not a literal that equals it today. A hardcoded 20 keeps its value
  # when the block's budget is lowered, so the one read that ignores the knob is
  # the one that outlives it.
  case "$fn" in
    *'timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}"'*) ;;
    *) echo "expected each read to take the block's read-budget knob" >&2; return 1 ;;
  esac
  case "$fn" in
    *'timeout -k 5 20'*) echo "expected no hardcoded bound to survive beside the knob" >&2; return 1 ;;
  esac
  # The listing is an apiserver request and carries the HTTP bound as well as
  # the wall-clock one; without it a client retrying against a wedged apiserver
  # is bounded only by the wrapper.
  reads=$(printf '%s\n' "$fn" | grep -c 'kubectl -n tenant-test get pods' || true)
  bounded=$(printf '%s\n' "$fn" | grep -A3 'kubectl -n tenant-test get pods' | grep -c -- '--request-timeout=' || true)
  if [ "$reads" -eq 0 ] || [ "$reads" -ne "$bounded" ]; then
    echo "every Pod listing must carry --request-timeout; $bounded of $reads do" >&2
    return 1
  fi
}

@test "the capture is declined by the phase budget rather than running unconditionally" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  gate=$(grep -n "^ *if cozy_diag_phase_has_time '(d) tenant worker CPU counters, sandbox node CPU time and worker per-thread CPU time'; then$" \
    "$lib" | head -n 1 | cut -d: -f1)
  call=$(grep -n '^ *cozy_capture_tenant_worker_thread_cpu "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$gate" ] || [ -z "$call" ]; then
    echo "expected the capture to sit behind a cozy_diag_phase_has_time gate" >&2
    return 1
  fi
  # THIS gate, not just some gate above the call. A line comparison alone is
  # satisfied by any earlier gate in the block, which would leave the capture
  # guarded by a decision taken about something else.
  next=$(awk -v g="$gate" 'NR > g && /cozy_diag_phase_has_time/ { print NR; exit }' "$lib")
  if [ -z "$next" ]; then
    echo "expected another phase gate after line $gate, so this test could bound the section" >&2
    return 1
  fi
  if [ "$call" -le "$gate" ] || [ "$call" -ge "$next" ]; then
    echo "the capture (line $call) must sit between its gate ($gate) and the next one ($next)" >&2
    return 1
  fi
}

@test "the capture shares the pass the other two subjects already wait for" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # A second wait would be paid on every failing run for a second rate over the
  # same window, and the window is already the scarce term: the phase declines
  # whatever has not started, so an added pause is taken out of the collectors
  # below it rather than out of slack.
  waits=$(grep -c '^ *sleep "\${COZY_DIAG_RATE_INTERVAL}"$' "$lib" || true)
  if [ "$waits" -ne 1 ]; then
    echo "expected exactly one measurement wait in the block, found $waits" >&2
    return 1
  fi
  loop=$(grep -n '^ *for _sample in 1 2; do$' "$lib" | head -n 1 | cut -d: -f1)
  done_line=$(awk -v l="$loop" 'NR > l && /^ *done$/ { print NR; exit }' "$lib")
  call=$(grep -n '^ *cozy_capture_tenant_worker_thread_cpu "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  for v in loop done_line call; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      return 1
    fi
  done
  if [ "$call" -le "$loop" ] || [ "$call" -ge "$done_line" ]; then
    echo "the capture (line $call) must run inside the existing sampling loop ($loop..$done_line)" >&2
    return 1
  fi
}

@test "the capture runs behind the console it could otherwise starve" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  call=$(grep -n '^ *cozy_capture_tenant_worker_thread_cpu "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$call" ] || [ -z "$console" ]; then
    echo "expected the failure path to call both the console and this capture" >&2
    return 1
  fi
  # Admission gates only when a collector may START, so a collector read twice
  # and placed ahead of the console can leave the console never started rather
  # than cut short -- and the console is the only surface that survives a worker
  # which never reached apid, the shape this failure usually takes.
  if [ "$call" -le "$console" ]; then
    echo "the thread capture (line $call) must run after the console capture (line $console)" >&2
    return 1
  fi
}
