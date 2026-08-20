#!/usr/bin/env bats
# Regression coverage for the two readings that price the layers ABOVE the
# sandbox nodes, in hack/e2e-chainsaw/_lib/run-kubernetes.sh: the runner
# kernel's own /proc/stat, and the QEMU threads of the sandbox VMs. cozytest.sh
# ends an @test block at the first bare closing brace, so command mocks stay at
# top level.
#
# What these two answer, and why nothing beside them answers it. Every other CPU
# reading in that file has a subject inside the sandbox -- a cgroup, a tenant
# worker's threads, a sandbox node's own /proc/stat -- and each of them can say a
# guest was given its ticks while getting no work done. None can say what the
# layers above spent, and that is where this suite's own tax is paid: the sandbox
# nodes' guest time is charged to the runner kernel as user time, and whatever
# the runner VM loses to the machine hosting IT is charged to nobody the sandbox
# can see. /proc/stat is not namespaced, so reading it from this container reads
# the runner kernel; the sandbox VMs are QEMU processes in this container's own
# PID namespace, so their threads are a local read too.
#
# So the failure these guard against is not a wrong number. It is a capture that
# reads the wrong layer, or an empty directory that a reader takes for a machine
# that spent nothing -- and the legends, which are what stop a reader comparing a
# hypervisor's row against its guest's.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-runner-cpu_test.bats
#           (or `bats hack/run-kubernetes-runner-cpu_test.bats` if the bats
#           binary is installed; cozytest.sh is the CI path.)

timeout_calls=/dev/null
timeout_rc_override=

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # Return 97 rather than running the command when the wrapper is not the
  # bounded form: a read that lost its ceiling must not pass as a read.
  [ "${1:-}" = -k ] || return 97
  shift 3
  "$@" || command_rc=$?
  [ -z "${timeout_rc_override}" ] || return "${timeout_rc_override}"
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
  cat "${file}" >&2
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
  # "I could not evaluate this"; folded together, a negative assertion is
  # satisfied by its own matcher giving up, which is the direction that goes
  # green and stays green.
  local _rc=0
  awk -v pattern="${pattern}" '$0 ~ pattern { found = 1 } END { exit found ? 0 : 1 }' "${file}" || _rc=$?
  case "${_rc}" in
    0)
      printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
      cat "${file}" >&2
      return 1
      ;;
    1) return 0 ;;
    *)
      printf 'awk could not evaluate pattern %s against %s\n' "${pattern}" "${file}" >&2
      return 1
      ;;
  esac
}

# The functions under test redirect a command's stderr into a report file and
# decide which artifact to write from it. cozytest.sh runs under `set -x`, so the
# tracer's own line for that command lands on the very stderr being redirected,
# and every assertion about those files would be reading the tracer. Call through
# these wrappers so the sinks hold what production writes.
run_kernel_capture() {
  local _rc=0
  ( set +x; cozy_capture_runner_kernel_cpu_time "${1:-1}" ) || _rc=$?
  return "${_rc}"
}

run_thread_capture() {
  local _rc=0
  ( set +x; cozy_capture_sandbox_qemu_thread_cpu "${1:-1}" ) || _rc=$?
  return "${_rc}"
}

# A /proc/stat shaped like the kernel's: the summed row first, one row per CPU,
# then the rows that are not CPU time at all. Two CPU rows are enough to tell the
# summed row from a per-CPU one; the walk does not count them.
#
# The numbers are the ones a loaded hypervisor produces: most of user here is
# guest, staged so the guest-inside-user accounting rule has something to bite
# on. How much of user is guest on a real runner is not a claim this fixture
# makes; the runner also runs the CI process tree, containerd and the suite.
stage_proc_stat() {
  local file="$1"
  mkdir -p "$(dirname "${file}")"
  {
    printf 'cpu  9000000 120 800000 4000000 30000 0 9000 45000 8900000 0\n'
    printf 'cpu0 4500000 60 400000 2000000 15000 0 4500 22000 4450000 0\n'
    printf 'cpu1 4500000 60 400000 2000000 15000 0 4500 23000 4450000 0\n'
    printf 'intr 900000000 0 0 0\n'
    printf 'ctxt 1800000000\n'
    printf 'btime 1700000000\n'
    printf 'processes 4000000\n'
  } >"${file}"
}

# A /proc shaped like this container's: three QEMU processes, each with an
# emulator thread and two vCPU threads. The vCPU thread name carries a space
# inside its parentheses, which is the whole reason the legend tells a reader to
# parse from the last ) -- a fixture without it would let a field-counting reader
# pass.
stage_sandbox_proc() {
  local root="$1"
  local pid tid
  for pid in 101 202 303; do
    mkdir -p "${root}/${pid}/task/${pid}"
    printf 'qemu-system-x86_64\n' >"${root}/${pid}/comm"
    printf '%s (qemu-system-x86) S 1 %s %s 0 -1 0 0 0 0 0 700 300 0 0 20 0 9 0 100 0 0\n' \
      "${pid}" "${pid}" "${pid}" >"${root}/${pid}/task/${pid}/stat"
    for tid in 1 2; do
      mkdir -p "${root}/${pid}/task/${pid}${tid}"
      printf '%s%s (CPU %s/KVM) R 1 %s %s 0 -1 0 0 0 0 0 5000 900 0 0 20 0 9 0 100 0 0\n' \
        "${pid}" "${tid}" "$((tid - 1))" "${pid}" "${pid}" \
        >"${root}/${pid}/task/${pid}${tid}/stat"
    done
  done
  # A process that is not QEMU, so the probe's filter is exercised rather than
  # assumed: this container also runs the shell driving the suite.
  mkdir -p "${root}/9/task/9"
  printf 'bash\n' >"${root}/9/comm"
  printf '9 (bash) S 1 9 9 0 -1 0 0 0 0 0 4 2 0 0 20 0 1 0 100 0 0\n' >"${root}/9/task/9/stat"
}

stage() {
  COZY_REPORT_DIR="$1/report"
  COZY_SNAPSHOT_NAME=runner-cpu-smoke
  COZY_DIAG_RUNNER_PROC_STAT="$1/proc-stat"
  COZY_DIAG_SANDBOX_PROC="$1/proc"
  stage_proc_stat "$COZY_DIAG_RUNNER_PROC_STAT"
  stage_sandbox_proc "$COZY_DIAG_SANDBOX_PROC"
}

kernel_path() {
  printf '%s/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-%s/proc-stat.txt' \
    "${COZY_REPORT_DIR}" "${1:-1}"
}

threads_path() {
  printf '%s/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu/sample-%s/qemu-threads.txt' \
    "${COZY_REPORT_DIR}" "${1:-1}"
}

# ---------------------------------------------------------------------------
# The runner kernel's own CPU time.
# ---------------------------------------------------------------------------

@test "the runner kernel CPU rows reach the capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  capture=$(kernel_path)
  assert_file_contains 'cpu  9000000 120 800000' "$capture"
  assert_file_contains 'cpu0 4500000' "$capture"
  rm -rf "$tmp"
}

@test "the capture says which layer its rows belong to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # The one reading that inverts every conclusion drawn from this file. Two
  # kernels in this report publish a /proc/stat -- the runner, and each sandbox
  # node one layer down -- so a reader who takes these rows for a sandbox node is
  # comparing a hypervisor against its own guest. The tenant worker has no
  # /proc/stat capture at all, which the legend now says rather than implying a
  # third file to go looking for.
  capture=$(kernel_path)
  assert_file_contains 'these rows belong to the RUNNER VM kernel' "$capture"
  assert_file_contains 'one layer above the sandbox nodes' "$capture"
  assert_file_contains 'sandbox-host-cpu-time' "$capture"
  # And that there is no third /proc/stat to go looking for. The clause sits in
  # parallel with one that names a directory, so an earlier wording sent a reader
  # hunting a tenant-worker /proc/stat that no capture in this report takes.
  assert_file_contains 'no capture in this report reads /proc/stat inside a tenant worker at all' "$capture"
  rm -rf "$tmp"
}

@test "the layer legend does not invent a capture that the report does not take" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # The two claims the legend makes about other readers, each pinned to the
  # source rather than to a count of the string: counting mentions of the file
  # also counts the legend itself, so a legend inventing a third reader kept
  # the count green (M21).
  capture=$(kernel_path)
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  awk '/^cozy_capture_sandbox_node_cpu_time\(\)/,/^}/' "$lib" | grep -q 'read /proc/stat' || {
    echo "FAIL: the legend points one layer down at a collector that no longer reads /proc/stat"
    false
  }
  if grep -n 'tenantkubeconfig' "$lib" | grep -q 'proc/stat'; then
    echo "FAIL: something reads /proc/stat through a tenant kubeconfig; the legend denies exactly that"
    false
  fi
  assert_file_lacks_pattern 'a tenant worker reports its own' "$capture"
  rm -rf "$tmp"
}

@test "the column legend travels with the numbers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  capture=$(kernel_path)
  # The column order, because the eighth and ninth are the two a reader wants
  # and are adjacent.
  assert_file_contains 'user nice system idle iowait irq softirq steal guest guest_nice' "$capture"
  # The two ordinals, by number rather than by the order of the words above: the
  # eighth and the ninth are adjacent and are the two a reader actually wants, so
  # a swapped pair turns a fraction of a percent of steal into twenty-odd. Pinned
  # because the column-order string alone stays green when the ordinals swap.
  assert_file_contains 'The eighth number is steal and the ninth is guest' "$capture"
  # And the accounting rule that makes this layer different from the one below:
  # guest is inside user, so adding them double-counts. Deliberately NOT a claim
  # about what fraction of this kernel's user time is guest -- the runner also
  # runs the CI process tree, and these rows are read against nothing that could
  # establish a proportion.
  assert_file_contains 'already counted inside user' "$capture"
  assert_file_lacks_pattern 'nearly all' "$capture"
  rm -rf "$tmp"
}

@test "the legend says what steal means on this layer and nowhere else" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # This is the number the whole capture exists for. A sandbox node reporting
  # steal 0 says the runner kernel served it promptly and says nothing about
  # whether the runner VM was itself waiting on the machine hosting it -- and
  # that second number appears in no other file in this report.
  capture=$(kernel_path)
  assert_file_contains 'steal on THIS layer' "$capture"
  assert_file_contains 'gave to somebody else' "$capture"
  # And read in ONE direction. A climbing steal proves this runner VM was
  # preempted; a zero proves nothing, because the column is filled only where the
  # hypervisor exposes a paravirt steal clock and this capture observes neither
  # the hypervisor nor that clock. The sandbox nodes one layer down can make the
  # stronger claim because this repository starts them with accel=kvm; nobody
  # here starts the runner VM.
  assert_file_contains 'read that column in ONE direction only' "$capture"
  assert_file_contains 'paravirt steal clock' "$capture"
  assert_file_lacks_pattern 'A steal of zero here is a reading and not an absence' "$capture"
  rm -rf "$tmp"
}

@test "the legend warns that the per-CPU rows are not physical cores" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # A cpuN row is a CPU as this kernel sees it, which is a hardware thread
  # wherever SMT is exposed. The capture reads no topology, so it says where that
  # answer lives rather than asserting which this lane is -- an artifact confident
  # about a machine it never looked at is worse than one that names the gap.
  capture=$(kernel_path)
  assert_file_contains 'What the row count is not is a core count' "$capture"
  assert_file_contains 'thread_siblings_list' "$capture"
  assert_file_lacks_pattern 'the lane runs SMT' "$capture"
  # And the online/possible split, which is why the rows can be fewer than the
  # total is summed over.
  assert_file_contains 'one per ONLINE CPU' "$capture"
  rm -rf "$tmp"
}

@test "the runner kernel read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # Local does not mean instant: this runs on the failure path of a machine
  # already wedged enough to lose a node join, and /proc is served by that same
  # kernel.
  assert_file_contains "-k 5 20 cat" "$timeout_calls"
  assert_file_contains '[capture exit code: 0]' "$(kernel_path)"
  rm -rf "$tmp"
}

@test "a lowered read budget moves the runner kernel bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Set after sourcing, which is how a caller and a test both set it. A value
  # read only at assignment time would reach `timeout` unchecked here.
  COZY_DIAG_READ_TIMEOUT=4

  run_kernel_capture

  assert_file_contains '-k 5 4 cat' "$timeout_calls"
  rm -rf "$tmp"
}

@test "a zero read budget is corrected rather than disabling the bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # `timeout -k 5 0` disables the timeout outright, so a zero assigned after
  # sourcing would restore the unbounded read this bound exists to remove.
  COZY_DIAG_READ_TIMEOUT=0

  run_kernel_capture

  assert_file_lacks_pattern '^-k 5 0 ' "$timeout_calls"
  assert_file_contains "-k 5 $COZY_DIAG_READ_TIMEOUT_DEFAULT cat" "$timeout_calls"
  rm -rf "$tmp"
}

@test "each runner kernel reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture

  # This pair is not taken inside one diagnostics block: the first reading is
  # taken before the node-join wait and the second only when that wait has run
  # out, so the interval between them is the whole join window rather than a
  # knob. A /proc/stat row carries no sample time, so nothing else in the
  # artifact records it.
  capture=$(kernel_path)
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the sample number decides which runner kernel reading a file is" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture 2

  # The pair is the instrument: one reading of a running total is an average over
  # the machine's whole uptime, not a rate over the failure. Two readings filed
  # under the same name would leave the second overwriting the first and the
  # artifact reading like a single sample.
  assert_file_contains 'cpu  9000000' "$(kernel_path 2)"
  [ ! -f "$(kernel_path 1)" ]
  rm -rf "$tmp"
}

@test "a file holding no cpu row at all is not filed as a reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # /proc/stat carries intr, ctxt and btime as well, so a read cut off before the
  # cpu rows leaves a file with numbers in it and no CPU time at all. Keyed on
  # size, that would be filed as a reading and the caller told a pair exists.
  {
    printf 'intr 900000000 0 0 0\n'
    printf 'ctxt 1800000000\n'
  } >"$COZY_DIAG_RUNNER_PROC_STAT"
  rc=0

  run_kernel_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(kernel_path)
  # `cat` exits zero for any file it could open, so this arrives on the CLEAN
  # exit path: the read did not fail, the file simply carries no CPU time. Said
  # in the file, because an empty-looking capture with exit code 0 otherwise
  # reads as a kernel that was idle.
  assert_file_contains 'the read completed and returned no cpu row at all' "$capture"
  assert_file_contains 'not a reading that the runner kernel was idle' "$capture"
  # And the pairing instruction must not ride on it: telling a reader to subtract
  # two files asserts both hold a reading, and following it here turns the
  # sibling's whole cumulative total into a rate over the window.
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a read that returned nothing is reported to the caller, not only to the report" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Killed before it produced a byte: the dominant shape of a bounded read that
  # ran out. Both callers ask this function whether it collected anything, and
  # one of them runs on the passing path where the report is the artifact nobody
  # downloads -- so a capture that got nothing has to answer non-zero or the
  # warning saying the pair is broken never prints.
  rm -f "$COZY_DIAG_RUNNER_PROC_STAT"
  rc=0

  run_kernel_capture || rc=$?

  [ "$rc" -ne 0 ]
  # The arm this staging reaches, not the substring all three unavailable arms
  # share: cat on a missing file fails with a message, so the note must point
  # at the failure marker holding it.
  assert_file_contains 'what it said is in the COLLECTION-FAILED.txt' "$(kernel_path)"
  rm -rf "$tmp"
}

@test "a kernel read that failed without a word says so, not something else" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The fourth corner: non-zero status, no rows, and silence on both streams.
  # Staged with an empty stat file and a forced status, since a real cat that
  # fails always says why. Unstaged, this arm and its wording were deletable
  # with the suite green.
  : >"$COZY_DIAG_RUNNER_PROC_STAT"
  timeout_rc_override=124
  rc=0

  run_kernel_capture || rc=$?
  timeout_rc_override=

  [ "$rc" -ne 0 ]
  assert_file_contains 'said nothing on either stream' "$(kernel_path)"
  rm -rf "$tmp"
}

@test "a thread walk that failed without a word says so, not something else" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The probe is stubbed to a silent non-zero exit: every real shape of the
  # probe leaves either thread lines or a finding, so the silent corner is
  # reachable only when the walk itself dies wordlessly.
  _cozy_thread_cpu_probe() { printf 'exit 3'; }
  rc=0

  run_thread_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(threads_path)
  assert_file_contains 'said nothing on either stream' "$capture"
  rm -rf "$tmp"
}

@test "a runner kernel read cut off part way is marked incomplete, not whole" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The walk wrote rows and then the wrapper killed it: the shape where a
  # whole-looking file is most misleading, since the difference against its
  # sibling would understate every row the read never reached.
  timeout_rc_override=124

  run_kernel_capture || true

  capture=$(kernel_path)
  assert_file_contains 'cpu  9000000' "$capture"
  assert_file_contains 'incomplete' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  assert_file_contains '[capture exit code: 124]' "$capture"
  # The lines that say how to read the numbers stay, because they do not depend
  # on the read having finished and this is exactly where a reader needs them.
  assert_file_contains 'RUNNER VM kernel' "$capture"
  rm -rf "$tmp"
}

@test "a partial runner kernel read is not reported to the caller as nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The other half of the pair above, and the reason the status is decided by
  # what reached the file rather than by the exit code alone: a read cut off
  # after it wrote rows left a usable capture, and a caller told nothing was
  # collected would contradict the file sitting beside it.
  timeout_rc_override=124
  rc=0

  run_kernel_capture || rc=$?

  [ "$rc" -eq 0 ]
  rm -rf "$tmp"
}

@test "a warning beside a healthy runner kernel read is not filed as a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A read that succeeds and still says something, which is the shape kubectl and
  # talosctl produce all over this collector's neighbourhood. Staged as a `cat`
  # on PATH rather than mocked as a function, because the collector runs it
  # through the `timeout` wrapper, which resolves it as a command.
  #
  # Which file the complaint lands in is decided by the STATUS, not by whether
  # stderr holds anything, so a healthy capture keeps its warning instead of
  # shipping a failure marker beside a reading that succeeded.
  mkdir -p "$tmp/bin"
  cat >"$tmp/bin/cat" <<'STUB'
#!/bin/sh
printf 'cat: (hint) this file is served by a pseudo filesystem\n' >&2
while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done <"$1"
STUB
  chmod +x "$tmp/bin/cat"
  PATH="$tmp/bin:$PATH"

  run_kernel_capture

  dir="$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-1"
  assert_file_contains 'cpu  9000000' "$dir/proc-stat.txt"
  assert_file_contains 'served by a pseudo filesystem' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  [ ! -f "$dir/read-error.log" ]
  rm -rf "$tmp"
}

@test "a failed runner kernel read sends its message to the failure marker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The other side of that routing rule, and the shape a missing pseudo file
  # produces: no row reached the capture and the reason exists. Sent beside the
  # capture rather than into it, and named as a failure rather than as a warning,
  # because this one did fail.
  rm -f "$COZY_DIAG_RUNNER_PROC_STAT"
  rc=0

  run_kernel_capture || rc=$?

  [ "$rc" -ne 0 ]
  dir="$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-1"
  assert_file_contains 'No such file' "$dir/COLLECTION-FAILED.txt"
  assert_file_contains 'COLLECTION-FAILED.txt beside this file' "$dir/proc-stat.txt"
  [ ! -f "$dir/READ-WARNINGS.txt" ]
  rm -rf "$tmp"
}

@test "a runner kernel report directory that cannot be created is said out loud" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A file where the directory has to go. Every write below it then fails, and
  # without a check the capture is a silent no-op -- the empty directory this
  # whole suite exists to refuse, produced by the one cause the markers cannot
  # describe, because there is nowhere to write a marker.
  mkdir -p "$COZY_REPORT_DIR/snapshots/runner-cpu-smoke"
  printf 'not a directory\n' >"$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/runner-kernel-cpu-time"
  rc=0

  out=$( ( set +x; cozy_capture_runner_kernel_cpu_time 1 ) 2>&1 ) || rc=$?

  [ "$rc" -ne 0 ]
  case "$out" in
    *"could not be created"*) ;;
    *)
      printf 'expected the collector to say the report directory could not be created, got: %s\n' "$out" >&2
      return 1
      ;;
  esac
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# The sandbox VMs' QEMU threads.
# ---------------------------------------------------------------------------

@test "the sandbox QEMU thread stat lines reach the capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  capture=$(threads_path)
  # All three sandbox VMs, because the question this answers is whether the
  # three nodes paid evenly: one node's vCPU threads pinned while its siblings
  # idle is a different fault from three nodes evenly loaded, and the kernel row
  # one layer up sums all three.
  assert_file_contains 'process 101 comm qemu-system-x86_64' "$capture"
  assert_file_contains 'process 202 comm qemu-system-x86_64' "$capture"
  assert_file_contains 'process 303 comm qemu-system-x86_64' "$capture"
  assert_file_contains '(CPU 0/KVM)' "$capture"
  # And nothing that is not QEMU: this container also runs the shell driving the
  # suite, and its threads are not what this measures.
  assert_file_lacks_pattern '\(bash\)' "$capture"
  rm -rf "$tmp"
}

@test "the thread capture says which layer its threads belong to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  # Two captures in this report read QEMU threads and they are one layer apart:
  # these are the sandbox VMs, and the ones under tenant-thread-cpu are the
  # workers running inside them. Read as the wrong one, a busy hypervisor looks
  # like a busy guest.
  capture=$(threads_path)
  assert_file_contains 'these threads belong to the SANDBOX VMs' "$capture"
  assert_file_contains 'tenant-thread-cpu' "$capture"
  assert_file_contains 'runner-kernel-cpu-time' "$capture"
  rm -rf "$tmp"
}

@test "the thread legend says to parse from the last closing parenthesis" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  # The trap that costs a reader the exact threads this capture exists to
  # identify: a vCPU thread is named `CPU 0/KVM`, that space sits inside the
  # parentheses, and every field counted over whitespace after it is off by one.
  capture=$(threads_path)
  assert_file_contains 'Parse from the last ) in the line instead' "$capture"
  assert_file_contains 'utime the 12th and stime the 13th' "$capture"
  rm -rf "$tmp"
}

@test "a container with no QEMU process is a finding rather than an empty capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The sandbox VMs are what the whole run stands on, so a container running none
  # of them is the most consequential thing this instrument can say -- and it
  # leaves exactly the same empty directory as a collector that failed.
  rm -rf "$COZY_DIAG_SANDBOX_PROC"
  mkdir -p "$COZY_DIAG_SANDBOX_PROC/9/task/9"
  printf 'bash\n' >"$COZY_DIAG_SANDBOX_PROC/9/comm"
  printf '9 (bash) S 1 9 9 0 -1 0 0 0 0 0 4 2 0 0 20 0 1 0 100 0 0\n' \
    >"$COZY_DIAG_SANDBOX_PROC/9/task/9/stat"
  rc=0

  run_thread_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(threads_path)
  assert_file_contains 'NO-QEMU-PROCESS' "$capture"
  # And the column legend must not ride on a file with no columns in it.
  assert_file_lacks_pattern 'Parse from the last' "$capture"
  rm -rf "$tmp"
}

@test "a proc mount that yields nothing is not reported as a container with no QEMU" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The two say different things about the run: one is a container that was read
  # and holds no hypervisor, the other is a read that never happened. Folded
  # together, a mount this collector could not open would be filed as a sandbox
  # with no VMs, which is a finding about the cluster it never observed.
  rm -rf "$COZY_DIAG_SANDBOX_PROC"
  mkdir -p "$COZY_DIAG_SANDBOX_PROC"
  rc=0

  run_thread_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(threads_path)
  assert_file_contains 'NO-PROC-READ' "$capture"
  assert_file_lacks_pattern 'NO-QEMU-PROCESS' "$capture"
  # The closing sentence follows the marker: a read that never happened must not
  # close with the no-QEMU verdict, whose subject the walk never observed.
  assert_file_contains 'says nothing about what the container was running' "$capture"
  assert_file_lacks_pattern 'a sandbox running no QEMU' "$capture"
  rm -rf "$tmp"
}

@test "a QEMU named and then gone is not filed as a reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A routine result on this failure path: the process was named and its task
  # directory was gone by the time the walk reached it. Without this the capture
  # holds a heading and no thread, which is enough bytes to look like a reading.
  for pid in 101 202 303; do
    rm -rf "$COZY_DIAG_SANDBOX_PROC/$pid/task"
  done
  # Pid files staged on purpose: this is the path where "which node still had a
  # usable pid file" is the whole answer, so the map must print here too.
  mkdir -p "$tmp/vmroot/srv1" "$tmp/vmroot/srv2" "$tmp/vmroot/srv3"
  printf '101\n' >"$tmp/vmroot/srv1/qemu.pid"
  printf '202\n' >"$tmp/vmroot/srv2/qemu.pid"
  printf '303\n' >"$tmp/vmroot/srv3/qemu.pid"
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/vmroot"
  rc=0

  run_thread_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(threads_path)
  assert_file_contains 'NO-THREAD-LINES' "$capture"
  assert_file_lacks_pattern 'Parse from the last' "$capture"
  # Three QEMUs were named two lines up, so the closing sentence must read as a
  # guest going away, never as a sandbox running no QEMU.
  assert_file_contains 'a guest going away under the walk' "$capture"
  assert_file_lacks_pattern 'a sandbox running no QEMU' "$capture"
  # And the node map still prints here: which node still had a usable pid file
  # is the question this path exists to answer.
  assert_file_contains 'is srv' "$capture"
  rm -rf "$tmp"
}

@test "the sandbox thread read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  assert_file_contains '-k 5 20 sh -c' "$timeout_calls"
  assert_file_contains '[capture exit code: 0]' "$(threads_path)"
  rm -rf "$tmp"
}

@test "the sample number decides which thread reading a file is" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture 2

  assert_file_contains 'process 101' "$(threads_path 2)"
  [ ! -f "$(threads_path 1)" ]
  rm -rf "$tmp"
}

@test "a thread walk cut off part way is marked incomplete rather than whole" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  timeout_rc_override=124

  run_thread_capture || true

  capture=$(threads_path)
  assert_file_contains 'process 101' "$capture"
  assert_file_contains 'incomplete' "$capture"
  assert_file_contains '[capture exit code: 124]' "$capture"
  rm -rf "$tmp"
}

@test "each thread reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  capture=$(threads_path)
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the thread capture reuses the probe the worker capture runs" {
  # A source check, and the point is the sharing rather than the text: the probe
  # already answers the two outcomes that matter here -- found QEMU, and QEMU is
  # gone -- and already names the three ways it can come up short. A second copy
  # would drift from this one exactly at those failure paths, which is where
  # nobody looks until it matters.
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  n=$(awk '/^cozy_capture_sandbox_qemu_thread_cpu\(\)/,/^}/' "$lib" | grep -cF '_cozy_thread_cpu_probe' || true)
  [ "$n" -ge 1 ] || {
    echo "FAIL: the sandbox thread capture no longer runs the shared probe"
    false
  }
  # And that there is still exactly one probe to share.
  d=$(grep -cE '^_cozy_thread_cpu_probe\(\)' "$lib" || true)
  [ "$d" -eq 1 ] || {
    echo "FAIL: found $d definitions of the thread probe; a second copy is the drift this test exists to refuse"
    false
  }
}

# ---------------------------------------------------------------------------
# Where the two readings of each pair sit.
# ---------------------------------------------------------------------------

@test "both new readings bracket the node-join wait on both paths" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # What these pairs measure is decided by where their calls sit, not by anything
  # inside the collectors: /proc/stat and a thread's utime are running totals, so
  # the interval a difference divides by is whatever runs between the readings.
  # The same reasoning the KVM pair beside them is held to, and the same failure:
  # a second reading placed after the happy-path tail prices ten to fifteen
  # minutes of storage and LoadBalancer work while the capture's own legend says
  # it priced the join.
  wait_line=$(grep -n '^  if ! timeout 18m bash -c' "$lib" | head -n 1 | cut -d: -f1)
  tail_line=$(grep -n '^  versions=\$(kubectl --kubeconfig' "$lib" | head -n 1 | cut -d: -f1)
  node_table=$(grep -n "cozy_diag_read 'tenant node table'" "$lib" | head -n 1 | cut -d: -f1)
  for v in wait_line tail_line node_table; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  for fn in cozy_capture_runner_kernel_cpu_time cozy_capture_sandbox_qemu_thread_cpu; do
    first=$(grep -n "${fn} 1" "$lib" | head -n 1 | cut -d: -f1)
    # The green second reading, found as the one after the wait rather than by
    # position in the file: the other call with the same argument is the failure
    # block's, and picking by count would be right only while the two happen to
    # be written in this order.
    green=$(awk -v w="$wait_line" -v f="${fn} 2" 'NR > w && index($0, f) { print NR; exit }' "$lib")
    red=$(awk -v w="$wait_line" -v f="${fn} 2" 'NR < w && index($0, f) { line = NR } END { if (line) print line }' "$lib")
    for v in first green red; do
      eval "n=\$$v"
      if [ -z "$n" ]; then
        echo "expected to find $v for $fn in $lib; without it this guard reports success for having lost its input" >&2
        return 1
      fi
    done
    if [ "$first" -ge "$wait_line" ]; then
      echo "$fn's first reading (line $first) is not taken before the node-join wait (line $wait_line), so the pair does not span it" >&2
      return 1
    fi
    if [ "$green" -le "$wait_line" ] || [ "$green" -ge "$tail_line" ]; then
      echo "$fn's passing-path second reading (line $green) does not sit between the wait (line $wait_line) and the happy-path tail (line $tail_line), so the green interval is not the join window the red one is compared against" >&2
      return 1
    fi
    if [ "$red" -ge "$node_table" ]; then
      echo "$fn's failure-path second reading (line $red) is taken after the diagnostics block has started reading the cluster (line $node_table), so the red interval is the wait plus whatever those reads cost" >&2
      return 1
    fi
  done
}

@test "the bringup names the QEMU threads the legend identifies by name" {
  # The legend reads vCPU threads by the name `CPU N/KVM`, and QEMU only
  # assigns those names when started with `-name ...,debug-threads=on`; without
  # the flag every thread carries the bare process name and the identification
  # the legend promises does not exist on this lane. Pinned on the qemu command
  # line itself, non-comment lines only, so a dropped flag fails here rather
  # than surfacing as an unexplained absence in production captures.
  awk '/^    qemu-system-x86_64 /,/-pidfile/' hack/e2e-prepare-cluster.bats |
    grep -v '^\s*#' | grep -q 'debug-threads=on' || {
    echo "FAIL: hack/e2e-prepare-cluster.bats starts QEMU without debug-threads=on; the thread names the legend documents will not exist" >&2
    false
  }
}

@test "the pair orders mirror around the wait so the KVM interval stays exact" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The KVM legend calls its interval the window and means it exactly; that is
  # only true while nothing runs between its samples and the wait. Its first
  # reading must therefore be the LAST of the three before the wait, and its
  # second the FIRST after it -- on both paths. The other two legends name the
  # sibling readings inside their intervals, so their order is free.
  wait_line=$(grep -n '^  if ! timeout 18m bash -c' "$lib" | head -n 1 | cut -d: -f1)
  [ -n "$wait_line" ] || { echo "FAIL: could not find the node-join wait"; false; }
  last_before=$(awk -v w="$wait_line" 'NR < w && /^  if ! cozy_capture_(sandbox_kvm_exits|runner_kernel_cpu_time|sandbox_qemu_thread_cpu) 1; then/ { l = $0 } END { print l }' "$lib")
  case "$last_before" in
    *cozy_capture_sandbox_kvm_exits*) ;;
    *) echo "FAIL: the reading closest to the wait from above is not the KVM pair: $last_before"; false ;;
  esac
  # The failure block is defined ABOVE the wait in file order and carries the
  # `|| true` form its budget guard requires; the green second readings are the
  # warned `if !` calls below the wait. Position in the file is the opposite of
  # position in time here, which is exactly why the forms are matched too.
  for scope in green red; do
    if [ "$scope" = green ]; then
      first_after=$(awk -v w="$wait_line" 'NR > w && /^  if ! cozy_capture_(sandbox_kvm_exits|runner_kernel_cpu_time|sandbox_qemu_thread_cpu) 2; then/ { print $0; exit }' "$lib")
    else
      first_after=$(awk -v w="$wait_line" 'NR < w && /^  cozy_capture_(sandbox_kvm_exits|runner_kernel_cpu_time|sandbox_qemu_thread_cpu) 2 \|\| true$/ { print $0; exit }' "$lib")
    fi
    case "$first_after" in
      *cozy_capture_sandbox_kvm_exits*) ;;
      *) echo "FAIL: the $scope second reading closest to the wait is not the KVM pair: $first_after"; false ;;
    esac
  done
}

@test "both new readings report a shortfall to the job log, not only to the report" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Two of the three call sites run outside the diagnostics block, and one of
  # those is the passing path -- where the report is the artifact nobody
  # downloads. A capture that collected nothing there has to say so where a
  # reader of a green run will see it, or the pair is silently broken for every
  # red run compared against it.
  for fn in cozy_capture_runner_kernel_cpu_time cozy_capture_sandbox_qemu_thread_cpu; do
    n=$(grep -cE "^  if ! ${fn} [12]; then" "$lib" || true)
    [ "$n" -eq 2 ] || {
      echo "FAIL: $fn has $n status-checked call sites; the two outside the failure block must both warn"
      false
    }
  done
  # And the warnings say WHICH reading is missing, matched on text specific to each
  # collector. Unscoped, these greps pass on the pre-existing KVM warnings, which
  # carry the same two phrases: emptying all four new warning bodies then left the
  # whole suite green.
  for subject in "the runner kernel's CPU time" "the sandbox VMs' QEMU threads"; do
    for when in before after; do
      n=$(grep -cF "WARNING: ${subject} yielded no reading ${when} the node-join wait" "$lib" || true)
      [ "$n" -eq 1 ] || {
        echo "FAIL: found $n warnings for ${subject} ${when} the wait; each reading warns once, in its own words"
        false
      }
    done
  done
}

@test "every legend line lands as one line rather than shredded into words" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_kernel_capture
  run_thread_capture

  # Each legend is one long single-quoted shell string, and an apostrophe inside
  # one ends the quote: the sentence then reaches the file as one word per line,
  # every `[` unmatched, and the capture still passes any assertion that greps for
  # a short phrase. This checks the shape instead -- a bracketed line opens and
  # closes on the same line -- because that is what a shredded legend loses.
  for capture in "$(kernel_path)" "$(threads_path)"; do
    if awk '/^\[/ && $0 !~ /\]$/ { print FILENAME ": " $0; found = 1 } END { exit found ? 1 : 0 }' "$capture"; then
      :
    else
      echo "FAIL: a bracketed legend line in $capture does not close on its own line, which is what an apostrophe inside its single quotes does to it" >&2
      return 1
    fi
    # And some bracketed line must exist at all: a requote that drops a whole
    # legend block leaves nothing for the shape check to object to.
    grep -q '^\[' "$capture" || {
      echo "FAIL: $capture carries no bracketed legend line at all" >&2
      return 1
    }
  done
  rm -rf "$tmp"
}

@test "the thread legend rule is executable, not just present" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_thread_capture

  # The legend tells a reader to parse from the last `)`. Nothing pinned that the
  # rule works on the lines this capture actually holds, so the fixture's space
  # inside `(CPU 0/KVM)` carried no assertion at all -- it only made the comment
  # sound justified. Applied here to a real captured line: after the last `)` the
  # state is field 1, utime field 12, stime field 13.
  line=$(grep -F '(CPU 0/KVM)' "$(threads_path)" | head -n 1)
  [ -n "$line" ] || {
    echo "FAIL: no vCPU thread line in the capture to apply the rule to"
    false
  }
  tail_fields=${line##*) }
  set -- $tail_fields
  [ "$1" = R ] || {
    echo "FAIL: field 1 after the last ) is '$1', expected the thread state"
    false
  }
  [ "${12}" = 5000 ] || {
    echo "FAIL: field 12 after the last ) is '${12}', expected utime 5000"
    false
  }
  [ "${13}" = 900 ] || {
    echo "FAIL: field 13 after the last ) is '${13}', expected stime 900"
    false
  }
  # And that counting over whitespace from the start of the line gets it WRONG,
  # which is the whole reason the rule exists: the space inside the parentheses
  # shifts every later field by one.
  set -- $line
  [ "${14}" != 5000 ] || {
    echo "FAIL: field 14 counted from the start equals utime, so this fixture no longer carries the trap the legend warns about"
    false
  }
  rm -rf "$tmp"
}

@test "the thread capture names which sandbox node each process is" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The pid files hack/e2e-prepare-cluster.bats writes when it starts the guests.
  # Without this map a reader has a container pid and no way to tell the
  # control-plane node from a worker-carrying one -- which is the discriminating
  # question, because the tenant workers sit on specific sandbox nodes.
  for srv in 1 2 3; do
    mkdir -p "$tmp/vmroot/srv${srv}"
  done
  printf '101\n' >"$tmp/vmroot/srv1/qemu.pid"
  printf '202\n' >"$tmp/vmroot/srv2/qemu.pid"
  printf '303\n' >"$tmp/vmroot/srv3/qemu.pid"
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/vmroot"

  run_thread_capture

  capture=$(threads_path)
  assert_file_contains '[process 101 is srv1]' "$capture"
  assert_file_contains '[process 202 is srv2]' "$capture"
  assert_file_contains '[process 303 is srv3]' "$capture"
  assert_file_lacks_pattern 'anonymous by node' "$capture"
  rm -rf "$tmp"
}

@test "a sandbox node whose pid file is gone is absent from the map, not renamed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Two of three pid files, which is what a node that died during the join looks
  # like from here. The map must not silently shift, since a wrong node name is
  # worse than no name: a triager would go and read the wrong guest console.
  mkdir -p "$tmp/vmroot/srv1" "$tmp/vmroot/srv3"
  printf '101\n' >"$tmp/vmroot/srv1/qemu.pid"
  printf '303\n' >"$tmp/vmroot/srv3/qemu.pid"
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/vmroot"

  run_thread_capture

  capture=$(threads_path)
  assert_file_contains '[process 101 is srv1]' "$capture"
  assert_file_contains '[process 303 is srv3]' "$capture"
  assert_file_lacks_pattern 'is srv2' "$capture"
  # And the legend says an absent node means an unreadable pid file rather than
  # leaving the gap to be read as a node that never existed.
  assert_file_contains 'had no usable pid file' "$capture"
  rm -rf "$tmp"
}

@test "a capture with no pid map says it is anonymous rather than implying a name" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # No pid files at all: a sandbox created some other way, or a tree where the
  # bringup wrote them elsewhere. The KVM capture beside this one discloses its
  # own anonymity in exactly this situation, and a reader who has seen that one
  # would otherwise take a process id here for a node identifier.
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/nowhere"

  run_thread_capture

  capture=$(threads_path)
  assert_file_contains 'anonymous by node' "$capture"
  assert_file_contains 'not which of them paid what' "$capture"
  assert_file_lacks_pattern 'is srv1' "$capture"
  rm -rf "$tmp"
}

@test "a pid file holding something that is not a pid is skipped, not trusted" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A truncated or half-written pid file, which is what a guest killed during
  # startup can leave. One node's file holds text and another's is empty, and
  # the asserts hold both forms out of the whole map line: an empty value slid
  # through validation once rendered as `[process srv1 is ]`, which a narrow
  # `is srv1` pattern read as clean (the surviving mutant both reviews named).
  mkdir -p "$tmp/vmroot/srv1" "$tmp/vmroot/srv2" "$tmp/vmroot/srv3"
  printf '\n' >"$tmp/vmroot/srv1/qemu.pid"
  printf 'not-a-pid\n' >"$tmp/vmroot/srv3/qemu.pid"
  printf '202\n' >"$tmp/vmroot/srv2/qemu.pid"
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/vmroot"

  run_thread_capture

  capture=$(threads_path)
  assert_file_contains '[process 202 is srv2]' "$capture"
  assert_file_lacks_pattern 'srv1' "$capture"
  assert_file_lacks_pattern 'not-a-pid' "$capture"
  assert_file_lacks_pattern 'is srv3' "$capture"
  # Exactly one map line: nothing rendered for the two bad files in any form.
  n=$(grep -c '^\[process ' "$capture" || true)
  [ "$n" -eq 1 ] || {
    echo "FAIL: expected exactly one map line, found $n"
    grep '^\[process ' "$capture"
    false
  }
  rm -rf "$tmp"
}

@test "a thread walk cut off after writing lines withholds the pairing instruction" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The half its sibling capture already pins: telling a reader to subtract two
  # files asserts both hold a whole reading. A walk cut off after the first node
  # would have the other two read as having spent nothing across the window,
  # which is the largest wrong number this artifact could produce.
  timeout_rc_override=124

  run_thread_capture || true

  capture=$(threads_path)
  assert_file_contains 'incomplete' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  # The lines that say how to read the numbers stay: they do not depend on the
  # walk having finished, and a cut-off capture is where a reader needs them.
  assert_file_contains 'Parse from the last ) in the line instead' "$capture"
  rm -rf "$tmp"
}

@test "a thread walk that ends clean with nothing to read says that is a finding" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The probe exits zero on a container it read and found no QEMU in, so this
  # arrives on the clean path with an exit code of 0 under it. Left silent, the
  # capture reads as a healthy reading of an idle machine -- while what it
  # actually records is that the three VMs the whole run stands on are not there.
  rm -rf "$COZY_DIAG_SANDBOX_PROC"
  mkdir -p "$COZY_DIAG_SANDBOX_PROC/9/task/9"
  printf 'bash\n' >"$COZY_DIAG_SANDBOX_PROC/9/comm"
  printf '9 (bash) S 1 9 9 0 -1 0 0 0 0 0 4 2 0 0 20 0 1 0 100 0 0\n' \
    >"$COZY_DIAG_SANDBOX_PROC/9/task/9/stat"

  run_thread_capture || true

  capture=$(threads_path)
  assert_file_contains 'NO-QEMU-PROCESS' "$capture"
  assert_file_contains 'the walk completed and produced no thread line at all' "$capture"
  assert_file_contains 'a finding about this run rather than a shortfall of this capture' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a failed thread walk sends its message to the failure marker its sibling uses" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # No thread line AND a message: the shape a walk killed against an unreadable
  # proc mount produces. It goes into COLLECTION-FAILED.txt, which is the name the
  # report's documentation gives the artifact-level failure marker and the name
  # the runner-kernel capture writes for this same shape -- a reader sweeping the
  # tarball for markers would otherwise miss this walk entirely.
  mkdir -p "$tmp/bin"
  cat >"$tmp/bin/sh" <<STUB
#!/bin/sh
printf 'sh: cannot open the proc mount\\n' >&2
exit 2
STUB
  chmod +x "$tmp/bin/sh"
  PATH="$tmp/bin:$PATH"
  rc=0

  run_thread_capture || rc=$?

  [ "$rc" -ne 0 ]
  dir="$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu/sample-1"
  assert_file_contains 'cannot open the proc mount' "$dir/COLLECTION-FAILED.txt"
  assert_file_contains 'COLLECTION-FAILED.txt beside this file' "$dir/qemu-threads.txt"
  [ ! -f "$dir/read-error.log" ]
  rm -rf "$tmp"
}

@test "a thread report directory that cannot be created is said out loud" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The one failure the markers cannot describe, because there is nowhere to put
  # one. Its sibling capture has this test; this one did not, which is the same
  # asymmetry as the failure marker above.
  mkdir -p "$COZY_REPORT_DIR/snapshots/runner-cpu-smoke"
  printf 'not a directory\n' >"$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu"
  rc=0

  out=$( ( set +x; cozy_capture_sandbox_qemu_thread_cpu 1 ) 2>&1 ) || rc=$?

  [ "$rc" -ne 0 ]
  case "$out" in
    *"could not be created"*) ;;
    *)
      printf 'expected the collector to say the report directory could not be created, got: %s\n' "$out" >&2
      return 1
      ;;
  esac
  rm -rf "$tmp"
}

@test "a runner kernel read that failed after writing rows points at its message" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Rows AND a non-zero status AND something on stderr: the fourth corner of the
  # pair of questions the arms answer, and the one no test staged. Without it the
  # arm that names read-error.log can be deleted with the suite green, leaving a
  # reader told the reading is partial and not told where the reason went.
  mkdir -p "$tmp/bin"
  cat >"$tmp/bin/cat" <<'STUB'
#!/bin/sh
while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done <"$1"
printf 'cat: the pseudo file stopped answering part way\n' >&2
exit 1
STUB
  chmod +x "$tmp/bin/cat"
  PATH="$tmp/bin:$PATH"

  run_kernel_capture || true

  dir="$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-1"
  assert_file_contains 'cpu  9000000' "$dir/proc-stat.txt"
  assert_file_contains 'read-error.log beside this file' "$dir/proc-stat.txt"
  assert_file_contains 'stopped answering part way' "$dir/read-error.log"
  rm -rf "$tmp"
}

@test "a thread walk that failed after writing lines points at its message" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The same fourth corner on the thread side. Its arm was unreachable from this
  # suite for the same reason: the only non-zero case drove the real probe, which
  # leaves stderr empty.
  mkdir -p "$tmp/bin"
  cat >"$tmp/bin/sh" <<STUB
#!/bin/sh
printf '101 (qemu-system-x86) S 1 101 101 0 -1 0 0 0 0 0 700 300 0 0 20 0 9 0 100 0 0\\n'
printf 'sh: the proc walk stopped part way\\n' >&2
exit 1
STUB
  chmod +x "$tmp/bin/sh"
  PATH="$tmp/bin:$PATH"

  run_thread_capture || true

  dir="$COZY_REPORT_DIR/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu/sample-1"
  assert_file_contains 'incomplete' "$dir/qemu-threads.txt"
  assert_file_contains 'read-error.log beside this file' "$dir/qemu-threads.txt"
  assert_file_contains 'stopped part way' "$dir/read-error.log"
  rm -rf "$tmp"
}

@test "both collectors still collect when timeout is not on PATH" {
  # The documented fallback, which every comparable suite beside this one pins:
  # where `timeout` is absent the reads run unbounded rather than not at all, and
  # the phase warning promises exactly that by name. `timeout` is a shell function
  # in this file, so `command -v timeout` is always true here and the else arm of
  # both collectors is otherwise never executed -- a broken redirect or a lost
  # `</dev/null` in there would surface only on a runner without coreutils, which
  # is the one machine the arm exists for.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in sh mkdir date cat grep sed awk rm mv printf; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in sh mkdir date cat grep rm; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  stage_proc_stat "$tmp/proc-stat"
  stage_sandbox_proc "$tmp/proc"
  rc=0

  # PATH is narrowed INSIDE the subprocess rather than in front of it: the shell
  # resolves the command name with the PATH the assignment sets, so a stripped
  # PATH in front of `bash` cannot find bash itself. The same trap the KVM suite
  # documents beside its own stripped-PATH test.
  out=$( ( set +x
    bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      COZY_REPORT_DIR='"$tmp"'/report
      COZY_SNAPSHOT_NAME=runner-cpu-smoke
      COZY_DIAG_RUNNER_PROC_STAT='"$tmp"'/proc-stat
      COZY_DIAG_SANDBOX_PROC='"$tmp"'/proc
      PATH='"$tmp"'/bin
      cozy_capture_runner_kernel_cpu_time 1
      cozy_capture_sandbox_qemu_thread_cpu 1
    ' ) 2>&1 ) || rc=$?

  [ "$rc" -eq 0 ] || {
    echo "FAIL: the collectors ended $rc with no timeout on PATH; the warning promises they keep collecting" >&2
    printf '%s\n' "$out" >&2
    false
  }
  assert_file_contains 'cpu  9000000' \
    "$tmp/report/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-1/proc-stat.txt"
  assert_file_contains 'process 101' \
    "$tmp/report/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu/sample-1/qemu-threads.txt"
  # And each capture says so itself. The phase warning that names unbounded
  # collectors fires inside the diagnostics phase, and two of this pair's three
  # call sites run outside it, so a capture that ran with no ceiling and stayed
  # quiet about it would read exactly like a bounded one.
  assert_file_contains '[bounds] timeout is not on PATH here' \
    "$tmp/report/snapshots/runner-cpu-smoke/runner-kernel-cpu-time/sample-1/proc-stat.txt"
  assert_file_contains '[bounds] timeout is not on PATH here' \
    "$tmp/report/snapshots/runner-cpu-smoke/sandbox-qemu-thread-cpu/sample-1/qemu-threads.txt"
  rm -rf "$tmp"
}

@test "a pid file that never yields a byte cannot hold the capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A FIFO with no writer: `[ -r ]` passes on it and the shell redirect open
  # would block forever, outside anything a wrapper can cover -- which is why
  # the read is an external cat under the read ceiling instead. Staged with a
  # low ceiling so the bounded path is what the test exercises; the outer
  # timeout is the M20 lesson from the identity suite -- a mutant that removes
  # the bound must fail this test, not hang the whole run.
  mkdir -p "$tmp/vmroot/srv1" "$tmp/vmroot/srv2"
  printf '101\n' >"$tmp/vmroot/srv1/qemu.pid"
  mkfifo "$tmp/vmroot/srv2/qemu.pid"
  COZY_DIAG_SANDBOX_VM_ROOT="$tmp/vmroot"
  COZY_DIAG_READ_TIMEOUT=1
  COZY_DIAG_READ_GRACE=1
  rc=0

  out=$( ( set +x; command timeout 20 bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      COZY_REPORT_DIR="'"$tmp"'/report"
      COZY_SNAPSHOT_NAME=runner-cpu-smoke
      COZY_DIAG_SANDBOX_PROC="'"$tmp"'/proc"
      COZY_DIAG_SANDBOX_VM_ROOT="'"$tmp"'/vmroot"
      COZY_DIAG_READ_TIMEOUT=1
      COZY_DIAG_READ_GRACE=1
      cozy_capture_sandbox_qemu_thread_cpu 1
    ' ) 2>&1 ) || rc=$?
  [ "$rc" -eq 0 ] || [ "$rc" -eq 124 ] || {
    echo "FAIL: the capture ended $rc; its output follows"
    printf '%s\n' "$out"
    false
  }

  [ "$rc" -ne 124 ] || {
    echo "FAIL: the capture hung on the FIFO until the outer timeout killed it"
    false
  }
  capture=$(threads_path)
  assert_file_contains '[process 101 is srv1]' "$capture"
  assert_file_lacks_pattern 'is srv2' "$capture"
  assert_file_contains 'had no usable pid file' "$capture"
  rm -rf "$tmp"
}
