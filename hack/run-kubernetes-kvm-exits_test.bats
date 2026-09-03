#!/usr/bin/env bats
# Regression coverage for the sandbox kernel's KVM exit counters in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why nothing beside it answers the same thing:
# every other CPU reading in this tree describes a subject inside the sandbox --
# a cgroup, a thread, a node's own /proc/stat -- and each of them can say a guest
# was given its ticks while the guest gets no work done. None of them can say
# what a tick cost. The tenant worker is a guest of a sandbox node, which is
# itself a guest of the runner, and the price of nesting is paid where the
# runner's kernel serves the sandbox VMs: exits, nested page faults and TLB
# flushes that no counter inside the sandbox can see. That kernel publishes them
# in debugfs, and the sandbox container shares it, so this is a local read.
#
# So the failure this suite guards against is not a wrong number; it is an empty
# directory, which reads as a kernel that paid nothing while nothing was ever
# asked.

timeout_calls=/dev/null
timeout_rc_override=
unshare_calls=/dev/null
unshare_rc_override=
mount_calls=/dev/null
mount_rc=0
mount_stderr=

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

# Mocked as a function rather than staged as a binary because the collector
# calls it in this shell. Its own flags are stripped and the rest is run, so the
# program the collector composed is the program this suite exercises -- the
# alternative, matching the program text here, would pass against a collector
# that had stopped composing it.
unshare() {
  local command_rc=0
  printf '%s\n' "$*" >>"${unshare_calls}"
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --mount | --propagation) shift ;;
      private) shift ;;
      *) break ;;
    esac
  done
  [ -z "${unshare_rc_override}" ] || return "${unshare_rc_override}"
  # The status is captured and returned rather than left as the last command's,
  # which is the same shape the wrapper above uses and not a matter of taste
  # here: cozytest.sh rewrites every closing brace in the first column into
  # `return 0` followed by the brace, so a mock ending on a bare command has its
  # status replaced by zero under that runner and kept under bats. This mock
  # exists to carry a non-zero mount refusal outward, so losing it turns the two
  # tests about a refused mount green against a collector that never saw one.
  "$@" || command_rc=$?
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
  # "I could not evaluate this"; folded together, a negative assertion is
  # satisfied by its own matcher giving up, which is the direction that goes
  # green and stays green.
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
  ( set +x; cozy_capture_sandbox_kvm_exits "${1:-1}" ) || _rc=$?
  return "${_rc}"
}

# `mount` is staged as an executable rather than mocked as a function: the
# collector runs it inside `sh -c`, a subprocess this shell's functions do not
# reach. A function here would be invisible exactly where the assertion needs
# it, and the walk would run against a mount that never happened without the
# suite noticing.
stage_mount_stub() {
  local dir="$1"
  mkdir -p "${dir}/bin"
  cat >"${dir}/bin/mount" <<STUB
#!/bin/sh
printf '%s\n' "\$*" >>"${dir}/mount.calls"
[ -z "${mount_stderr}" ] || printf '%s\n' "${mount_stderr}" >&2
exit ${mount_rc}
STUB
  chmod +x "${dir}/bin/mount"
  mount_calls="${dir}/mount.calls"
  PATH="${dir}/bin:${PATH}"
}

# A tree of files shaped like the kernel's, because staging a subject worth
# reading needs a hypervisor. Two global counters and one per-VM directory is
# enough to tell the two levels apart; the walk does not count them.
stage_debugfs() {
  local root="$1"
  mkdir -p "${root}/kvm/4242-13"
  printf '1200\n' >"${root}/kvm/exits"
  printf '640\n' >"${root}/kvm/pf_taken"
  printf '31\n' >"${root}/kvm/tlb_flush"
  printf '800\n' >"${root}/kvm/4242-13/exits"
  printf '17\n' >"${root}/kvm/4242-13/halt_exits"
  # The shape the kernel actually puts in every per-VM directory beside the
  # counters, reproduced rather than imagined: mmu_rmaps_stat is a seq_file
  # printing a tab-separated histogram table. Without it in the fixture nothing
  # here is shaped like the directory the walk meets in production, and both
  # skip paths are unreachable from this suite.
  {
    printf 'Rmap_Count:\t0\t1\t2-3\t4-7\n'
    printf 'Level=4K:\t6291000\t400\t50\t6\n'
    printf 'Level=2M:\t12200\t88\t0\t0\n'
    printf 'Level=1G:\t24\t0\t0\t0\n'
  } >"${root}/kvm/4242-13/mmu_rmaps_stat"
}

stage() {
  COZY_REPORT_DIR="$1/report"
  COZY_SNAPSHOT_NAME=kvm-smoke
  COZY_DIAG_KVM_DEBUGFS="$1/debugfs"
  stage_debugfs "$1/debugfs"
  stage_mount_stub "$1"
}

capture_path() {
  printf '%s/snapshots/kvm-smoke/sandbox-kvm-exits/sample-%s/kvm-stats.txt' \
    "${COZY_REPORT_DIR}" "${1:-1}"
}

marker_path() {
  printf '%s/snapshots/kvm-smoke/sandbox-kvm-exits/sample-%s/COLLECTION-FAILED.txt' \
    "${COZY_REPORT_DIR}" "${1:-1}"
}

@test "the kernel's own KVM counters are read through a debugfs of this capture's own" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  # The mount is what makes the counters reachable at all, and the namespace is
  # what keeps it from being a change to the sandbox: the suite runs on for
  # another twenty minutes after this, and a mount left behind would be this
  # collector editing the environment the rest of the run measures.
  assert_file_contains '--mount --propagation private' "$unshare_calls"
  assert_file_contains '-t debugfs' "$mount_calls"
  capture=$(capture_path)
  assert_file_contains 'exits 1200' "$capture"
  assert_file_contains 'tlb_flush 31' "$capture"
  # What the walk found, counted at both levels and over the directories it
  # opened. Three numbers rather than one because they answer different
  # questions -- a kernel hosting no VM has counters and no directories, a VM
  # that went away under the walk leaves a directory and no counters -- and
  # without this the directory count and the guard that produces it are carried
  # by nothing.
  assert_file_contains 'counters read: 3 kernel-wide and 2 per-VM, across 1 per-VM director' "$capture"
  assert_file_lacks_pattern 'unknown' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unavailable' "$capture"
  rm -rf "$tmp"
}

@test "a file the kernel computes on read is skipped by name and never opened" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  # mmu_rmaps_stat is not a counter and, unlike every other file here, reading
  # it is not free: it is registered with single_open and seq_read, so its
  # contents are computed on the first read, under the VM slots_lock and MMU
  # write lock, walking every rmap head. A shape test cannot save that -- to see
  # the shape you must read it, and the read is the cost -- so it is skipped by
  # name before anything opens it. The two skip reasons are worded differently
  # on purpose: this assertion fails, rather than silently passing on the shape
  # path, if the name check is ever dropped.
  capture=$(capture_path)
  assert_file_contains '[skipped vm 4242-13] mmu_rmaps_stat: not opened' "$capture"
  assert_file_lacks_pattern 'Rmap_Count' "$capture"
  assert_file_lacks_pattern 'Level=4K' "$capture"
  # And it is not counted as a per-VM counter either.
  assert_file_contains 'counters read: 3 kernel-wide and 2 per-VM' "$capture"
  rm -rf "$tmp"
}

@test "a file that is not a counter at all is skipped by its shape" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The general net behind the name list. A counter is one integer and nothing
  # else; anything else in these directories would otherwise be emitted under a
  # heading promising `level name value`, counted as a counter, and then offered
  # to the reader as something to difference against its sibling.
  {
    printf 'header one two\n'
    printf 'row 1 2\n'
  } >"$tmp/debugfs/kvm/some_table"
  printf 'not-a-number\n' >"$tmp/debugfs/kvm/some_text"
  # A number followed by more of anything: the leading integer alone would
  # pass, so this pins that the shape check reads the whole content rather than
  # the first line. Without this fixture a check narrowed to the first line
  # stays green, and a table whose first row happens to be a bare number would
  # be filed as a counter.
  {
    printf '42\n'
    printf 'and then some\n'
  } >"$tmp/debugfs/kvm/some_prefixed_table"

  run_capture

  capture=$(capture_path)
  assert_file_contains '[skipped global] some_table: not a single integer' "$capture"
  assert_file_contains '[skipped global] some_text: not a single integer' "$capture"
  assert_file_contains '[skipped global] some_prefixed_table: not a single integer' "$capture"
  assert_file_lacks_pattern 'header one two' "$capture"
  assert_file_lacks_pattern 'not-a-number' "$capture"
  assert_file_lacks_pattern 'some_prefixed_table 42' "$capture"
  # Still three kernel-wide counters: neither of the two joined them.
  assert_file_contains 'counters read: 3 kernel-wide' "$capture"
  rm -rf "$tmp"
}

@test "a file that cannot be read is skipped as unread, not called a non-counter" {
  [ "$(id -u)" -ne 0 ] || skip "mode bits do not stop root from opening the file"
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # In production the failure is a live counter whose VM is being torn down:
  # its entry is still listed while the kernel refuses the open, or refuses
  # the read once the entry is detached. Neither state can be staged with a
  # plain file, so an unreadable mode stands in; what the walk sees is the
  # same, a listed file that yields no content.
  printf '99\n' >"$tmp/debugfs/kvm/locked_out"
  chmod 000 "$tmp/debugfs/kvm/locked_out"

  run_capture

  capture=$(capture_path)
  assert_file_contains '[skipped global] locked_out: could not be read' "$capture"
  assert_file_lacks_pattern 'locked_out: not a single integer' "$capture"
  # Not read means not counted: still the three counters the fixture stages.
  assert_file_contains 'counters read: 3 kernel-wide' "$capture"
  chmod 700 "$tmp/debugfs/kvm/locked_out"
  rm -rf "$tmp"
}

@test "the per-VM split is kept apart from the kernel-wide totals" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  # Both levels exist in debugfs and they answer different questions: the totals
  # say what this kernel paid, the per-VM directories say whether the three
  # sandbox VMs paid it evenly. Folded together, a single VM burning exits looks
  # like a kernel under uniform load, which is the reading this split exists to
  # refuse.
  capture=$(capture_path)
  assert_file_contains '[vm 4242-13] exits 800' "$capture"
  assert_file_contains '[global] exits 1200' "$capture"
  rm -rf "$tmp"
}

@test "the counter legend travels with the numbers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  capture=$(capture_path)
  # Which layer these numbers describe. The counters belong to the kernel
  # hosting the sandbox VMs, so they price the sandbox's own execution and say
  # nothing about what a tenant worker's guest did to its sandbox node -- and a
  # reader who takes them for the second draws the opposite conclusion.
  assert_file_contains 'the sandbox VMs and not what runs inside them' "$capture"
  # Which of them a difference may be taken of. Subtracting an instantaneous
  # gauge from its sibling reading yields a number with no meaning, and the
  # names sit next to the cumulative ones in the same directory.
  # Both of the vCPU-state gauges, not just the first. They sit in the same
  # directory as everything else and under the same kind of name, and a list
  # that names one of them reads as the closed set of things not to difference.
  assert_file_contains 'guest_mode' "$capture"
  assert_file_contains 'blocking' "$capture"
  assert_file_contains 'cumulative' "$capture"
  # The counter a reader will look for and not find, named where they look.
  assert_file_contains 'no ept_violation counter' "$capture"
  # Why the per-VM directories cannot be attributed to a sandbox node.
  assert_file_contains 'cannot map' "$capture"
  # The pairing instruction rides with the numbers on the one arm that holds a
  # whole reading, as it does for the node CPU time capture beside it.
  assert_file_contains 'subtracting this file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "each reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  # This pair is not taken inside one diagnostics block: the first reading is
  # taken before the node-join wait and the second only when that wait has run
  # out, so the interval between them is the whole join window rather than a
  # knob. Nothing else in the artifact records it.
  capture=$(capture_path)
  assert_file_contains 'read attempted from' "$capture"
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture

  # Local does not mean instant: a mount and a walk over debugfs both enter the
  # kernel, and this one runs on the failure path of a host already wedged
  # enough to lose a node join.
  assert_file_contains '-k 5 20 unshare' "$timeout_calls"
  assert_file_contains '[capture exit code: 0]' "$(capture_path)"
  rm -rf "$tmp"
}

@test "a lowered read budget moves this collector's bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # Set after sourcing, which is how a caller and a test both set it. A value
  # read only at assignment time would reach `timeout` unchecked here.
  COZY_DIAG_READ_TIMEOUT=4

  run_capture

  assert_file_contains '-k 5 4 unshare' "$timeout_calls"
  rm -rf "$tmp"
}

@test "a zero read budget is corrected rather than disabling the bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # `timeout -k 5 0` disables the timeout outright, so a zero assigned after
  # sourcing would restore the unbounded read this bound exists to remove.
  COZY_DIAG_READ_TIMEOUT=0

  run_capture

  assert_file_lacks_pattern '^-k 5 0 ' "$timeout_calls"
  assert_file_contains "-k 5 $COZY_DIAG_READ_TIMEOUT_DEFAULT unshare" "$timeout_calls"
  rm -rf "$tmp"
}

@test "no unshare on PATH is recorded rather than left as an empty directory" {
  # A subprocess with a stripped PATH, because this file mocks `unshare` as a
  # shell function and `command -v` finds functions -- the arm cannot be reached
  # from inside a test body. The PATH is staged rather than emptied: the
  # collector shells out before it gets to the check, so an empty PATH would
  # fail this for the wrong reason and read as the arm being broken.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  # /sbin and /usr/sbin are in the list because `mount` lives there on the host
  # this suite is also run from, and a staging loop that cannot find it fails
  # the test rather than quietly leaving the check to run against a PATH that
  # is missing more than the one command it is about.
  for c in mkdir grep rm mount date; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/unshare" ]; then
    echo "FAIL: unshare leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  rc=0

  # Assigned inside the subprocess rather than in front of it: bash resolves the
  # command name with the PATH the assignment sets, so `PATH=... bash` cannot
  # find bash itself.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=kvm-smoke
    PATH='"$tmp"'/bin
    cozy_capture_sandbox_kvm_exits 1
  ' 2>&1) || rc=$?
  printf '%s\n' "$out" >"$tmp/out"

  [ "$rc" -ne 0 ]
  marker="$tmp/report/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1/COLLECTION-FAILED.txt"
  assert_file_contains 'unshare is not on PATH' "$marker"
  # The distinction the marker exists for, spelled out in the artifact: a tool
  # that was never run says nothing about the kernel, and the reading a missing
  # file invites is that nesting cost it nothing.
  assert_file_contains 'not a reading that nesting cost it nothing' "$marker"
  rm -rf "$tmp"
}

@test "no mount on PATH is recorded rather than left as an empty directory" {
  # The sibling of the unshare check above. Staged the same way and for the same
  # reason, with one difference: `unshare` has to be present for the collector
  # to reach the check this test is about, and the host running this suite may
  # have no such binary, so a stub stands in. It is never executed -- the
  # collector returns at the check below it.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in mkdir grep rm mv date; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  printf '#!/bin/sh\nexit 91\n' >"$tmp/bin/unshare"
  chmod +x "$tmp/bin/unshare"
  if [ -e "$tmp/bin/mount" ]; then
    echo "FAIL: mount leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  rc=0

  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=kvm-smoke
    PATH='"$tmp"'/bin
    cozy_capture_sandbox_kvm_exits 1
  ' 2>&1) || rc=$?
  printf '%s\n' "$out" >"$tmp/out"

  [ "$rc" -ne 0 ]
  marker="$tmp/report/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1/COLLECTION-FAILED.txt"
  assert_file_contains 'mount is not on PATH' "$marker"
  assert_file_contains 'not a reading that nesting cost it nothing' "$marker"
  rm -rf "$tmp"
}

@test "a mount that was refused is not reported as a kernel running no guests" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_rc=32
  mount_stderr='mount: /sys/kernel/debug: permission denied.'
  stage "$tmp"
  rc=0

  run_capture || rc=$?

  # The two readings this has to keep apart: a sandbox that would not let the
  # capture at debugfs, and a kernel whose KVM is idle. Both leave no counters,
  # and only the second is a statement about the run.
  [ "$rc" -ne 0 ]
  marker=$(marker_path)
  assert_file_contains 'permission denied' "$marker"
  assert_file_contains 'debugfs could not be mounted' "$marker"
  assert_file_lacks_pattern 'no KVM' "$marker"
  rm -rf "$tmp"
}

@test "a kernel with no kvm directory is a finding rather than an empty capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  # A hint on stderr beside the finding, so the arm that routes stderr by status
  # is exercised for this code and not only for its sibling. The kernel answered
  # here, so the complaint belongs in a file named as a warning; routed with the
  # failures it would put read-error.log next to a capture that did not fail.
  mount_stderr='mount: (hint) debugfs is already mounted here.'
  stage "$tmp"
  rm -rf "$tmp/debugfs/kvm"
  rc=0

  run_capture || rc=$?

  # Answered to the caller and not only to the report, because one of the two
  # callers runs on the passing path, whose report is the artifact nobody
  # downloads, and this is the most consequential thing the instrument can say.
  [ "$rc" -ne 0 ]
  # Not a shortfall of the capture. The sandbox VMs are started with
  # accel=kvm by hack/e2e-prepare-cluster.bats, so a kernel with no kvm
  # directory under debugfs is a kernel that ran them some other way -- which
  # would explain the slowness this whole line of measurement is chasing, and
  # must not be filed as a collector that came up short.
  capture=$(capture_path)
  assert_file_contains 'NO-KVM-DIR' "$capture"
  assert_file_contains 'accel=kvm' "$capture"
  # And the legend must not ride on it. A file carrying a finding and no
  # counters cannot be differenced against its sibling, and an instruction to do
  # so would turn the sibling's whole cumulative total into a rate over the
  # window -- the largest number this artifact could possibly produce, arrived
  # at by following its own advice.
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  # And a finding is not also a truncated walk, nor a read that came back with
  # nothing. All three sentences fit in this file and they contradict each
  # other: one says the kernel answered and had no KVM, the second says the walk
  # was cut off before it could tell, the third says the read never got that
  # far. Only the first is true here, and the other two describe an instrument
  # that failed rather than one that has something to report.
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unavailable' "$capture"
  # The probe's own status, read out of the capture rather than inferred from
  # what the legend did or did not say. The collector decides four things from
  # this code -- which file stderr lands in, whether the legend rides, whether
  # the pairing instruction rides, what it returns -- so a probe that stopped
  # emitting it would change all four, and a test that only watches the effects
  # can be made blind by any one guard added upstream of them.
  assert_file_contains '[capture exit code: 4]' "$capture"
  dir="$COZY_REPORT_DIR/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1"
  assert_file_contains 'debugfs is already mounted here' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/read-error.log" ]
  rm -rf "$tmp"
}

@test "a kvm directory holding only per-VM counters is not reported as holding none" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The kernel-wide files gone and one VM directory left. Both levels are
  # optional in their own way -- a kernel hosting no VM has no per-VM
  # directories -- so the emptiness test has to be about the two together. Read
  # as either-or it reports a capture that holds counters as one that holds
  # none, and the arm that says so is the one a reader trusts over the numbers
  # printed above it.
  rm -f "$tmp"/debugfs/kvm/exits "$tmp"/debugfs/kvm/pf_taken "$tmp"/debugfs/kvm/tlb_flush

  run_capture

  capture=$(capture_path)
  assert_file_contains '[vm 4242-13] exits 800' "$capture"
  assert_file_lacks_pattern 'NO-COUNTER-FILES' "$capture"
  rm -rf "$tmp"
}

@test "a kvm directory holding no counter is not filed as a reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm"
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(capture_path)
  assert_file_contains 'NO-COUNTER-FILES' "$capture"
  # And the legend must not ride on it: telling a reader to subtract two files
  # asserts that both hold a whole reading, and this one holds none.
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unavailable' "$capture"
  rm -rf "$tmp"
}

@test "a warning beside a finding is filed as a warning, not as a failed read" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_stderr='mount: (hint) debugfs is already mounted here.'
  stage "$tmp"
  # A kernel that answered and had no counters, while mount also wrote a hint.
  # The status decides which file the complaint lands in, and the two findings
  # are on the side that succeeded: routing them with the failures would put a
  # file named read-error.log next to a capture that reports what the kernel
  # said, which is the failure-shaped name this routing exists to keep off a
  # reading that did not fail.
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm"
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  dir="$COZY_REPORT_DIR/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1"
  assert_file_contains 'NO-COUNTER-FILES' "$dir/kvm-stats.txt"
  assert_file_contains '[capture exit code: 5]' "$dir/kvm-stats.txt"
  assert_file_contains 'debugfs is already mounted here' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/read-error.log" ]
  rm -rf "$tmp"
}

@test "a capture holding no counter carries no legend about how to read counters" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The legend rides on any capture that HOLDS numbers, which is wider than the
  # captures that hold a whole reading -- and the widening has a floor. Every
  # line of it describes numbers: which layer they belong to, which of them may
  # be differenced, what the per-VM tag does and does not identify. On a file
  # with no numbers the most consequential of those lines invites a reader to
  # difference a counter that is not there, which is the sibling's entire
  # cumulative total read as a rate over the window.
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm"

  run_capture || true

  capture=$(capture_path)
  assert_file_lacks_pattern 'a difference against the sibling sample' "$capture"
  assert_file_lacks_pattern 'no ept_violation counter' "$capture"
  assert_file_lacks_pattern 'per-VM and anonymous' "$capture"
  rm -rf "$tmp"
}

@test "a kvm directory holding only kernel-wide counters is not reported as holding none" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The mirror of the per-VM case beside this one, and the production shape
  # whenever the sandbox VMs are down: the kernel keeps its own totals and hosts
  # no VM, so there is no per-VM directory at all. The emptiness test is about
  # the two levels together, and read as either-or this direction reports a
  # capture holding three counters as one holding none.
  rm -rf "$tmp/debugfs/kvm/4242-13"

  run_capture

  capture=$(capture_path)
  assert_file_contains '[global] exits 1200' "$capture"
  assert_file_lacks_pattern 'NO-COUNTER-FILES' "$capture"
  rm -rf "$tmp"
}

@test "a counter written without a trailing newline is read rather than passed over" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # `read` returns non-zero on a last line with no newline while still setting
  # the variable, so a walk that trusted its status would drop the value it had
  # just read. The kernel formats its own stat files through "%llu\n" and so
  # does not produce this shape, which is exactly why nothing else here would
  # exercise the branch that handles it.
  # One at each level, because the fallback is written twice -- once per walk --
  # and a fixture at one level leaves the other's copy free to be deleted with
  # the suite still green.
  printf '7' >"$tmp/debugfs/kvm/nested_run"
  printf '5' >"$tmp/debugfs/kvm/4242-13/irq_exits"

  run_capture

  assert_file_contains '[global] nested_run 7' "$(capture_path)"
  assert_file_contains '[vm 4242-13] irq_exits 5' "$(capture_path)"
  rm -rf "$tmp"
}

@test "a per-VM directory holding no readable counter is not filed as a reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # A VM directory whose counters cannot be read is the shape a VM going away
  # under the walk produces, and it is indistinguishable on disk from a VM that
  # reported nothing. Counting directories rather than counters lets it satisfy
  # the emptiness test: the capture then holds no number and still carries the
  # instruction to difference it against its sibling, which would turn that
  # sibling's whole cumulative total into a rate over the window.
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm/4242-13"
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(capture_path)
  assert_file_contains 'NO-COUNTER-FILES' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unavailable' "$capture"
  rm -rf "$tmp"
}

@test "a read killed before it reached a counter is not presented as a partial reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The walk writes a heading before it reads anything, so a read killed on its
  # first blocked file leaves a file that is not empty and holds no number. That
  # shape is staged here by walking a kvm directory with nothing in it and
  # forcing the wrapper's status, since what the arm has to get right is the
  # file, not how it came to look that way. Keyed on file size rather than on a
  # counter, the capture would be labelled partial and the caller told a pair
  # was collected.
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm"
  timeout_rc_override=124
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(capture_path)
  assert_file_contains 'no counter at all' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a read that returned nothing is reported to the caller, not only to the report" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # Killed before it produced a byte: the dominant shape of a bounded read that
  # ran out. Both callers ask this function whether it collected anything, and
  # one of them runs on the passing path where the report is the artifact nobody
  # downloads -- so a capture that got nothing has to answer non-zero or the
  # warning that says the pair is broken never prints.
  unshare_rc_override=124
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(capture_path)
  assert_file_contains 'produced no counter at all' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a partial read is not reported to the caller as nothing collected" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The other half of the pair above, and the reason the status is decided by
  # what reached the file rather than by the exit code alone: a walk cut off
  # after it wrote counters left a usable capture, and a caller told that
  # nothing was collected would contradict the artifact sitting beside it.
  timeout_rc_override=124
  rc=0

  run_capture || rc=$?

  [ "$rc" -eq 0 ]
  assert_file_contains 'incomplete' "$(capture_path)"
  rm -rf "$tmp"
}

@test "a capture that failed points at the error log written beside it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_stderr='mount: (hint) debugfs is already mounted here.'
  stage "$tmp"
  # A read that was cut short while something was also written to stderr: the
  # message belongs beside the capture rather than in the job log of a run that
  # may be days old, and an arm that reports the shortfall without saying where
  # the message went sends the reader looking there anyway. The node CPU time
  # capture beside this one names its own error file for the same reason.
  timeout_rc_override=124

  run_capture || true

  dir="$COZY_REPORT_DIR/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1"
  assert_file_contains 'read-error.log' "$dir/kvm-stats.txt"
  assert_file_contains 'debugfs is already mounted here' "$dir/read-error.log"
  rm -rf "$tmp"
}

@test "a read that returned no counter but left a message points at where it went" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_stderr='mount: (hint) debugfs is already mounted here.'
  stage "$tmp"
  # The fourth corner of the pair of questions the arms answer: no counter
  # reached the file, and something was written to stderr. The three other
  # corners are staged elsewhere in this file, and without this one the arm that
  # names the error log can be deleted with the suite still green -- leaving a
  # reader told the capture is unavailable and not told that the reason is
  # sitting in the file next to it.
  rm -rf "$tmp/debugfs/kvm"
  mkdir -p "$tmp/debugfs/kvm"
  timeout_rc_override=124
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  capture=$(capture_path)
  assert_file_contains 'no counter at all' "$capture"
  assert_file_contains 'read-error.log' "$capture"
  rm -rf "$tmp"
}

@test "a mount that refused without a word does not point at a message that is not there" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_rc=32
  mount_stderr=
  stage "$tmp"
  rc=0

  run_capture || rc=$?

  # mount almost always writes a reason, which is why the marker was written to
  # point at one. When it does not, the marker is a single line telling the
  # reader to look above it at nothing, and a reader who follows that concludes
  # the artifact lost the message rather than that there never was one.
  [ "$rc" -ne 0 ]
  marker=$(marker_path)
  assert_file_contains 'without a word on either stream' "$marker"
  assert_file_lacks_pattern 'the message above' "$marker"
  rm -rf "$tmp"
}

@test "a read cut off part way is marked incomplete rather than presented whole" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # The walk wrote counters and then the wrapper killed it: the dominant shape
  # of a bounded read that ran out, and the one where a whole-looking file is
  # most misleading, since the difference against its sibling would understate
  # every counter the walk never reached.
  timeout_rc_override=124

  run_capture || true

  capture=$(capture_path)
  assert_file_contains 'exits 1200' "$capture"
  assert_file_contains 'incomplete' "$capture"
  assert_file_lacks_pattern 'subtracting this file from its sibling' "$capture"
  assert_file_contains '[capture exit code: 124]' "$capture"
  # The lines that say how to read the numbers stay, because they do not depend
  # on the walk having finished and this is exactly where a reader needs them:
  # the counters above are real, and the naming trap and the anonymity of the
  # per-VM split apply to them whether or not the walk reached the end. Only the
  # instruction to difference two files asserts completeness.
  assert_file_contains 'no ept_violation counter' "$capture"
  assert_file_contains 'per-VM and anonymous' "$capture"
  rm -rf "$tmp"
}

@test "a warning beside a healthy read is not filed as a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  mount_stderr='mount: (hint) source is ignored for debugfs.'
  stage "$tmp"

  run_capture

  # mount writes hints to stderr and exits zero, exactly as kubectl and talosctl
  # do. Which file the complaint lands in is decided by the status, so a healthy
  # capture keeps its warning instead of shipping a failure marker beside a
  # reading that succeeded.
  dir="$COZY_REPORT_DIR/snapshots/kvm-smoke/sandbox-kvm-exits/sample-1"
  assert_file_contains 'source is ignored for debugfs' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  assert_file_contains 'exits 1200' "$dir/kvm-stats.txt"
  rm -rf "$tmp"
}

@test "a report directory that cannot be created is said out loud" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"
  # A file where the directory has to go. Every write below it then fails, and
  # without a check the capture is a silent no-op -- the empty directory this
  # whole suite exists to refuse, produced by the one cause the markers cannot
  # describe, because there is nowhere to write a marker.
  mkdir -p "$COZY_REPORT_DIR/snapshots/kvm-smoke"
  printf 'not a directory\n' >"$COZY_REPORT_DIR/snapshots/kvm-smoke/sandbox-kvm-exits"
  rc=0

  out=$( ( set +x; cozy_capture_sandbox_kvm_exits 1 ) 2>&1 ) || rc=$?

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

@test "the sample number decides which reading a file belongs to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  unshare_calls="$tmp/unshare.calls"
  stage "$tmp"

  run_capture 2

  # The pair is the instrument: one reading of a running total is an average
  # over its whole life, not a rate over the failure. Two readings filed under
  # the same name would leave the
  # second overwriting the first and the artifact reading like a single sample.
  assert_file_contains 'exits 1200' "$(capture_path 2)"
  [ ! -f "$(capture_path 1)" ]
  rm -rf "$tmp"
}
