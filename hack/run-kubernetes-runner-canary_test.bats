#!/usr/bin/env bats
# Regression coverage for the runner fixed-work canary in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this collector is for, and why its failure modes are worth pinning. No
# counter that file already reads carries a unit of work of its own, whatever
# unit it is read in; this one runs a fixed amount of work and times it, which
# is what gives its reading a scale of its own and lets it call a single red run
# pathological with no green run beside it. That scale is exactly what a wrong
# duration destroys, and a wrong duration is cheap to produce: a clock field
# parsed with the wrong number of decimals is off by a factor of ten, which is
# the size of the effect being hunted, and a capture reporting it would read like
# a finding rather than like a bug.
#
# So most of what follows is about the arithmetic and about the difference
# between silence and a reading. An arm nobody could time, an arm stopped at its
# ceiling and an arm that ran to the end are three different things, and only
# the last of them is a rate.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-runner-canary_test.bats
#
# cozytest.sh is the canonical runner and the CI path: it runs this file under
# `sh` with `set -eu`, and in CI that `sh` is dash, which is what the collector
# meets in the sandbox image. `bats` runs the same file under bash without those
# flags, so it is a convenience rather than an equal alternative: the guards
# here that exist for dash semantics do not fire under it. Spelling the capture
# truncation `: >` again -- the regression the guard below is written for --
# leaves bats green, and leaves cozytest green too wherever /bin/sh is not dash,
# which includes macOS, where it is bash in POSIX mode. Run `dash
# hack/cozytest.sh` to exercise those guards off CI.

timeout_calls=/dev/null
timeout_rc_override=
timeout_rc_match=
timeout_skip_command=

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # Return 97 rather than running the command when the wrapper is not the
  # bounded form: work that lost its ceiling must not pass as work that had one.
  [ "${1:-}" = -k ] || return 97
  shift 3
  if [ -z "${timeout_skip_command}" ]; then
    "$@" || command_rc=$?
  fi
  case "$*" in
    *"${timeout_rc_match}"*)
      [ -z "${timeout_rc_override}" ] || return "${timeout_rc_override}"
      ;;
  esac
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

# The clock the arms are timed by, replaced by a scripted sequence.
#
# Real durations are the one thing this suite must not depend on: they are set
# by whatever else is running on the machine, and every rate below is derived
# from them. The stub hands out one centisecond stamp per call in the order the
# collector takes them -- arm one start, arm one end, arm two start, arm two end
# -- so the arithmetic the capture prints is decided by the test rather than by
# the host.
#
# Installed by a function called after the library is sourced, because sourcing
# would otherwise replace it with the real one.
stamp_values=
stamp_index=0
stamp_fail_at=

stub_stamp() {
  _cozy_canary_stamp() {
    stamp_index=$(( stamp_index + 1 ))
    if [ "${stamp_index}" = "${stamp_fail_at}" ]; then
      return 1
    fi
    _COZY_CANARY_CS=$(printf '%s\n' ${stamp_values} | sed -n "${stamp_index}p")
    [ -n "${_COZY_CANARY_CS}" ] || return 1
  }
}

# The same collector run in this shell rather than in a subshell, for the cases
# that read a variable it leaves behind rather than a file: the alert the call
# sites branch on would go with the subshell.
run_canary_here() {
  local _rc=0
  set +x
  cozy_capture_runner_canary "${1:-1}" || _rc=$?
  return "${_rc}"
}

# The collector writes into files it decides from a command's stderr, and
# cozytest.sh runs under `set -x`, so the tracer's own lines would land on the
# very stderr being redirected. Call through this so the sinks hold what
# production writes.
run_canary() {
  local _rc=0
  ( set +x; cozy_capture_runner_canary "${1:-1}" ) || _rc=$?
  return "${_rc}"
}

stage() {
  COZY_REPORT_DIR="$1/report"
  COZY_SNAPSHOT_NAME=canary-smoke
  COZY_DIAG_RUNNER_PROC_UPTIME="$1/uptime"
  printf '70062.90 774208.84\n' >"${COZY_DIAG_RUNNER_PROC_UPTIME}"
}

capture_path() {
  printf '%s/snapshots/canary-smoke/runner-canary/sample-%s/fixed-work.txt' \
    "${COZY_REPORT_DIR}" "${1:-1}"
}

# ---------------------------------------------------------------------------
# The clock.
# ---------------------------------------------------------------------------

@test "the clock reads the kernel hundredths into centiseconds" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  stage "$tmp"

  _cozy_canary_stamp
  [ "$_COZY_CANARY_CS" = 7006290 ] || {
    echo "FAIL: 70062.90 read as $_COZY_CANARY_CS centiseconds" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "a clock field the arithmetic is not written for is refused rather than parsed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  stage "$tmp"

  # None of these is a shape the arithmetic below is written for, and they fail
  # in three different ways once the checks are gone: a fraction of the wrong
  # width divides by ten or multiplies by it, which is the size of the effect
  # being hunted; a field with no point at all reads its own digits as
  # hundredths and skews by a percent; a non-digit ends the shell mid-arm. A
  # refused clock is silence; a mis-scaled one is a finding nobody made.
  for field in '70062.900' '70062.9' '70062' '70' 'x.90' '70062.9x' '070062.90' '7x.90'; do
    printf '%s 774208.84\n' "$field" >"$COZY_DIAG_RUNNER_PROC_UPTIME"
    _COZY_CANARY_CS=unset
    if _cozy_canary_stamp; then
      echo "FAIL: the clock accepted the field $field and read it as $_COZY_CANARY_CS" >&2
      return 1
    fi
  done
  rm -rf "$tmp"
}

@test "a leading zero in the hundredths is not read as octal" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  stage "$tmp"

  # 08 and 09 are not numbers to `$(( ))` at all, so an unstripped fraction ends
  # the shell on one uptime in fifty rather than returning a wrong figure. There
  # is no third kind to exercise: the field is two digits, so a leading zero can
  # only make 00 through 09, and every one of those that parses at all parses as
  # the value it reads as -- 07 is seven in both shells with the strip and
  # without it. So the rows are the two that are errors, and each pins the
  # figure the strip has to leave behind as well as the error it avoids.
  for pair in '70062.09 7006209' '70062.08 7006208'; do
    field=${pair% *}
    want=${pair#* }
    printf '%s 774208.84\n' "$field" >"$COZY_DIAG_RUNNER_PROC_UPTIME"
    _cozy_canary_stamp
    [ "$_COZY_CANARY_CS" = "$want" ] || {
      echo "FAIL: $field read as $_COZY_CANARY_CS, expected $want" >&2
      return 1
    }
  done
  rm -rf "$tmp"
}

@test "an unreadable clock is refused without putting a shell error on stderr" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  stage "$tmp"
  COZY_DIAG_RUNNER_PROC_UPTIME="$tmp/absent"

  rc=0
  # The redirect goes INSIDE the subshell, after the tracer is off. cozytest.sh
  # runs under `set -x`, and a sink attached to the subshell itself collects the
  # tracer's own line for `set +x` before it takes effect -- so the assertion
  # below would be reading the runner rather than the function.
  ( set +x; _cozy_canary_stamp 2>"$tmp/err" ) || rc=$?
  [ "$rc" -ne 0 ] || {
    echo "FAIL: a missing clock file was accepted as a stamp" >&2
    return 1
  }
  # The function reports by returning. A shell error here reaches the job log of
  # a run whose every other line is written to mean something exact, which is
  # what teaches the next reader to skim.
  [ ! -s "$tmp/err" ] || {
    echo "FAIL: the missing clock file put this on stderr:" >&2
    cat "$tmp/err" >&2
    return 1
  }
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# The ceiling around each arm.
# ---------------------------------------------------------------------------

@test "each arm runs under the canary ceiling rather than under the read bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='100 150 150 250'
  # Set apart on purpose. The canary is work rather than a read, and a read
  # bound lowered to speed the diagnostics up would kill an arm that is meant to
  # take seconds; taking the wrong one of these two is invisible until that
  # happens.
  COZY_CANARY_RUN_BOUND=11
  COZY_DIAG_READ_TIMEOUT=7
  COZY_DIAG_READ_GRACE=3

  run_canary

  assert_file_contains '-k 3 11 awk' "$timeout_calls"
  assert_file_contains '-k 3 11 dd' "$timeout_calls"
  assert_file_lacks_pattern '-k 3 7 ' "$timeout_calls"
  rm -rf "$tmp"
}

@test "a zero canary ceiling is rejected because zero is no ceiling at all" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='100 150 150 250'
  COZY_CANARY_RUN_BOUND=0
  COZY_DIAG_READ_GRACE=3

  ( set +x; run_canary 2>"$tmp/err" )

  # The default, not the rejected value: a warning that named the fallback and
  # then used the number anyway would be worse than no warning.
  assert_file_contains "COZY_CANARY_RUN_BOUND='0'" "$tmp/err"
  assert_file_contains "-k 3 ${COZY_CANARY_RUN_BOUND_DEFAULT} awk" "$timeout_calls"
  rm -rf "$tmp"
}

@test "a run that never set the canary ceiling is not warned about ignoring one" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  # Re-sourced inside the subshell with the knob unset, because that is the
  # state of every real run: nobody sets COZY_CANARY_RUN_BOUND, the source-time
  # assignment settles it to the default, and the collector's re-validation
  # must then find a value rather than an empty string. Without the assignment
  # the validator warns about ignoring a value nobody supplied, on the
  # » WARNING: prefix CI readers grep for, on every run including green ones.
  ( set +x
    unset COZY_CANARY_RUN_BOUND
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_CANARY_CPU_ITERATIONS=1000
    COZY_CANARY_MEM_BLOCK_MIB=1
    COZY_CANARY_MEM_BLOCKS=1
    cozy_capture_runner_canary 1
  ) >"$tmp/out" 2>"$tmp/err"

  assert_file_lacks_pattern 'ignoring COZY_CANARY_RUN_BOUND' "$tmp/err"
  # And the default reached the arms as a real ceiling, so the silence above is
  # the knob settling rather than the warning merely going missing.
  assert_file_contains "-k 5 ${COZY_CANARY_RUN_BOUND_DEFAULT} awk" "$timeout_calls"
  rm -rf "$tmp"
}

@test "the arms still run when timeout is not on PATH, and the capture says so" {
  # The documented fallback: where `timeout` is absent the work runs unbounded
  # rather than not at all, and the phase warning promises exactly that by name.
  # `timeout` is a shell function in this file, so `command -v timeout` is always
  # true here and the else arm is otherwise never executed -- which is the arm
  # that runs on a machine without coreutils, the one machine it exists for.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in sh mkdir date cat grep sed awk dd rm mv printf; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in sh mkdir date cat awk dd rm; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  printf '70062.90 774208.84\n' >"$tmp/uptime"
  # The clock here is a file, because /proc/uptime is not on every machine this
  # suite runs on. A file does not advance on its own, so both stamps of an arm
  # would read one value, every arm would take the sub-tick branch, and the
  # division this case exists to exercise would never run. So awk and dd are
  # wrapped: the wrapper advances the clock by a fixed step and then execs the
  # real binary, which keeps the work real while the duration each arm reports
  # is decided here rather than by whatever machine this is. Removed before
  # writing rather than written over: these are symlinks, and a redirect would
  # land in the binary they point at.
  real_awk=$(readlink "$tmp/bin/awk")
  real_dd=$(readlink "$tmp/bin/dd")
  rm -f "$tmp/bin/awk" "$tmp/bin/dd"
  cat >"$tmp/bin/awk" <<EOF
#!/bin/sh
printf '70063.90 774208.84\n' >"$tmp/uptime"
exec "$real_awk" "\$@"
EOF
  cat >"$tmp/bin/dd" <<EOF
#!/bin/sh
printf '70064.90 774208.84\n' >"$tmp/uptime"
exec "$real_dd" "\$@"
EOF
  chmod +x "$tmp/bin/awk" "$tmp/bin/dd"
  rc=0

  # PATH is narrowed INSIDE the subprocess rather than in front of it: the shell
  # resolves the command name with the PATH the assignment sets, so a stripped
  # PATH in front of `bash` cannot find bash itself.
  out=$( ( set +x
    bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      COZY_REPORT_DIR='"$tmp"'/report
      COZY_SNAPSHOT_NAME=canary-smoke
      COZY_DIAG_RUNNER_PROC_UPTIME='"$tmp"'/uptime
      COZY_CANARY_CPU_ITERATIONS=1000
      COZY_CANARY_MEM_BLOCK_MIB=1
      COZY_CANARY_MEM_BLOCKS=1
      PATH='"$tmp"'/bin
      cozy_capture_runner_canary 1
    ' ) 2>&1 ) || rc=$?

  [ "$rc" -eq 0 ] || {
    echo "FAIL: the canary ended $rc with no timeout on PATH; the warning promises it keeps collecting" >&2
    printf '%s\n' "$out" >&2
    false
  }
  capture="$tmp/report/snapshots/canary-smoke/runner-canary/sample-1/fixed-work.txt"
  # Both arms ran the real binaries, and the wrapped clock gives each of them a
  # second, so each reports a duration and divides by it: 1000 iterations and
  # 1 MiB, each in one second. That is what makes this the case that exercises
  # the unbounded arm end to end rather than only reaching it.
  assert_file_contains 'arm: compute' "$capture"
  assert_file_contains 'arm: memory' "$capture"
  assert_file_contains 'elapsed: 1000 ms' "$capture"
  assert_file_contains 'rate: 1000 iterations per second' "$capture"
  assert_file_contains 'rate: 1 MiB per second' "$capture"
  assert_file_lacks_pattern 'produced no reading' "$capture"
  assert_file_lacks_pattern 'finished inside one tick' "$capture"
  # And the capture says it ran without a ceiling. The phase warning that names
  # unbounded collectors fires inside the diagnostics phase, and one of this
  # pair of samples is taken outside it on every run, so a capture that ran
  # unbounded and stayed quiet about it would read exactly like a bounded one.
  assert_file_contains '[bounds] timeout is not on PATH here' "$capture"
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# What a duration divides to.
# ---------------------------------------------------------------------------

@test "an arm that ran with no ceiling is not reported as having hit one" {
  # The else arm of the `command -v timeout` branch, driven to a status the
  # ceiling also produces. Nothing bounded this arm, so its elapsed time cannot
  # be attributed to a bound: on a machine without coreutils an outside kill
  # landing past the bound would otherwise be published as the
  # too-slow-to-finish finding this collector exists to detect.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in sh mkdir date cat grep sed rm dd printf; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in sh mkdir date rm dd; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  printf '70062.90 774208.84\n' >"$tmp/uptime"
  # The compute arm's own command, replaced by one that moves the clock 21
  # seconds past the start stamp -- one second past the twenty-second bound the
  # collector would have applied had there been a timeout to apply it with --
  # and then dies the way an outside kill does.
  cat >"$tmp/bin/awk" <<STUB
#!/bin/sh
printf '70083.90 774208.84\n' >"$tmp/uptime"
exit 137
STUB
  chmod +x "$tmp/bin/awk"
  rc=0

  out=$( ( set +x
    bash -c '
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      COZY_REPORT_DIR='"$tmp"'/report
      COZY_SNAPSHOT_NAME=canary-smoke
      COZY_DIAG_RUNNER_PROC_UPTIME='"$tmp"'/uptime
      COZY_CANARY_MEM_BLOCK_MIB=1
      COZY_CANARY_MEM_BLOCKS=1
      COZY_CANARY_RUN_BOUND=20
      PATH='"$tmp"'/bin
      cozy_capture_runner_canary 1
    ' ) 2>&1 ) || rc=$?

  # The memory arm beside it returned without failing, so the collector still
  # reports success: an arm that died is not the collector failing. That arm
  # takes both its stamps after the stub moved the clock, so what it contributes
  # is the sub-tick note rather than a rate.
  [ "$rc" -eq 0 ] || {
    echo "FAIL: the canary ended $rc with one arm killed and the other returning cleanly; the call sites read that status as no reading at all" >&2
    printf '%s\n' "$out" >&2
    false
  }
  capture="$tmp/report/snapshots/canary-smoke/runner-canary/sample-1/fixed-work.txt"
  assert_file_contains 'ended with status 137 with no ceiling in play' "$capture"
  # The whole point of the sentence above: the bound was never in play, so the
  # elapsed time cannot be read as the bound firing.
  assert_file_lacks_pattern 'stopped at the' "$capture"
  rm -rf "$tmp"
}

@test "an unbounded arm ending at a ceiling status survives the flags production runs under" {
  # `_COZY_CANARY_BOUNDED` is read where a 124 or 137 is classified, and the
  # suites source this library under `set -eu`. It is initialised at the top of
  # every arm rather than only on the branch that sets it to 1, so the read on
  # the unbounded path finds a value; without that initialisation this path dies
  # on an unset variable instead of reporting, and on the failing path the death
  # takes the rest of the diagnostics block with it. The case above drives the
  # same path without the flags, so it cannot see this.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in sh mkdir date cat grep sed rm dd printf; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin /sbin /usr/sbin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in sh mkdir date rm dd; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  if [ -e "$tmp/bin/timeout" ]; then
    echo "FAIL: timeout leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  printf '70062.90 774208.84\n' >"$tmp/uptime"
  # Exits with the status the ceiling also produces, which is the only status
  # that reaches the read this case exists for.
  cat >"$tmp/bin/awk" <<STUB
#!/bin/sh
printf '70083.90 774208.84\n' >"$tmp/uptime"
exit 137
STUB
  chmod +x "$tmp/bin/awk"
  rc=0

  out=$( ( set +x
    bash -c '
      set -eu
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      COZY_REPORT_DIR='"$tmp"'/report
      COZY_SNAPSHOT_NAME=canary-smoke
      COZY_DIAG_RUNNER_PROC_UPTIME='"$tmp"'/uptime
      COZY_CANARY_MEM_BLOCK_MIB=1
      COZY_CANARY_MEM_BLOCKS=1
      COZY_CANARY_RUN_BOUND=20
      PATH='"$tmp"'/bin
      cozy_capture_runner_canary 1
    ' ) 2>&1 ) || rc=$?

  [ "$rc" -eq 0 ] || {
    echo "FAIL: under set -eu the canary ended $rc on the unbounded path; an unset read here aborts the collector instead of reporting" >&2
    printf '%s\n' "$out" >&2
    false
  }
  case "$out" in
    *'_COZY_CANARY_BOUNDED'*)
      echo "FAIL: the unbounded path named _COZY_CANARY_BOUNDED on stderr, which is what an unset read looks like under set -u" >&2
      printf '%s\n' "$out" >&2
      return 1
      ;;
  esac
  capture="$tmp/report/snapshots/canary-smoke/runner-canary/sample-1/fixed-work.txt"
  assert_file_contains 'ended with status 137 with no ceiling in play' "$capture"
  rm -rf "$tmp"
}

@test "the note about a missing ceiling is written only when the ceiling was missing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  COZY_CANARY_CPU_ITERATIONS=1000000
  COZY_CANARY_MEM_BLOCK_MIB=2048
  COZY_CANARY_MEM_BLOCKS=4

  run_canary

  capture=$(capture_path)
  # `timeout` is a shell function in this file, so both arms here ran under a
  # ceiling. A note saying they did not would tell a reader the figures above it
  # were taken with no bound -- on every run, which is how a note that is not
  # conditional reads.
  assert_file_lacks_pattern 'timeout is not on PATH here' "$capture"
  assert_file_contains 'rate: 2000000 iterations per second' "$capture"
  rm -rf "$tmp"
}

@test "a completed arm reports its duration and the rate that duration divides to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # 50 centiseconds for the compute arm, 200 for the memory one.
  stamp_values='1000 1050 1050 1250'
  COZY_CANARY_CPU_ITERATIONS=1000000
  COZY_CANARY_MEM_BLOCK_MIB=2
  COZY_CANARY_MEM_BLOCKS=4

  run_canary

  capture=$(capture_path)
  assert_file_contains 'elapsed: 500 ms' "$capture"
  assert_file_contains 'rate: 2000000 iterations per second' "$capture"
  assert_file_contains 'elapsed: 2000 ms' "$capture"
  # The memory rate is per MiB of the work actually done, so it follows both
  # sizes rather than the block size alone: 2 MiB four times over in 2s.
  assert_file_contains 'rate: 4 MiB per second' "$capture"
  rm -rf "$tmp"
}

@test "the memory arm streams the block size the capture reports it in" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  COZY_CANARY_MEM_BLOCK_MIB=2
  COZY_CANARY_MEM_BLOCKS=4

  run_canary

  # Bytes rather than a suffix, and the conversion is what makes the reported
  # MiB the MiB that were written: a `2M` here would be two million bytes to one
  # dd, two mebibytes to another and an error to a third, and the rate above
  # would be wrong by that ratio in the first case with nothing to show for it.
  assert_file_contains 'bs=2097152 count=4' "$timeout_calls"
  rm -rf "$tmp"
}

@test "an arm stopped at its ceiling is recorded as a bound rather than as a rate" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 3050'
  COZY_CANARY_MEM_BLOCK_MIB=100
  COZY_CANARY_MEM_BLOCKS=8
  COZY_CANARY_RUN_BOUND=20

  run_canary

  capture=$(capture_path)
  assert_file_contains 'work: 800 MiB' "$capture"
  # The ceiling firing is the strongest reading this arm produces, so it is kept
  # and labelled rather than dropped: the work did not finish inside that many
  # seconds, which is already a multiple of what it should take. Reported as a
  # rate it would be an average over work that did not all happen.
  assert_file_contains 'this arm did not finish: it was stopped at the 20s ceiling' "$capture"
  assert_file_contains 'BELOW 40 MiB per second' "$capture"
  assert_file_lacks_pattern '^rate: [0-9]+ MiB' "$capture"
  # And the compute arm beside it, which did finish, still reports a rate: a
  # collector that gave up on both arms because one hit its ceiling would lose
  # the half that discriminates.
  assert_file_contains 'iterations per second' "$capture"
  rm -rf "$tmp"
}

@test "a kill before the ceiling is not reported as the ceiling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=137
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  COZY_CANARY_RUN_BOUND=20

  run_canary

  capture=$(capture_path)
  # 137 is what the ceiling's follow-up kill produces and also what the OOM
  # killer produces, and the elapsed line is the only thing that separates
  # them: an arm that ended two seconds into a twenty-second bound was not
  # stopped by the bound. Read by status alone, this was published as the
  # too-slow-to-finish finding the instrument exists to detect, manufactured
  # by whatever sent the kill.
  assert_file_contains 'ended with status 137 after 2000 ms, before the 20s ceiling could have fired' "$capture"
  assert_file_lacks_pattern 'stopped at the' "$capture"
  assert_file_lacks_pattern 'BELOW' "$capture"
  rm -rf "$tmp"
}

@test "a kill that landed at the ceiling is recorded as the ceiling and named as a kill" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=137
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 3050'
  COZY_CANARY_MEM_BLOCK_MIB=100
  COZY_CANARY_MEM_BLOCKS=8
  COZY_CANARY_RUN_BOUND=20

  run_canary

  capture=$(capture_path)
  # A status of 137 says a kill ended the arm where 124 says the term signal
  # did, which is a different observation the way the sibling collectors name a
  # kill apart from a deadline -- and it is still the ceiling, because the whole
  # bound was on the clock when the kill landed. Whose kill it was is not in the
  # status, so the sentence does not name one.
  assert_file_contains 'it was stopped at the 20s ceiling, on a status that says a kill ended the arm without saying whose' "$capture"
  assert_file_contains 'BELOW 40 MiB per second' "$capture"
  rm -rf "$tmp"
}

@test "both arms stopped at the ceiling raise the alert rather than a shortfall" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  # Both arms on the clock for the whole bound, which is the pathology this
  # collector exists to detect at its loudest. A ceiling-stopped arm carries a
  # bound rather than a rate, but it is still a reading: counted as nothing, the
  # sample would report a shortfall and the call sites would print that they got
  # no reading, which is the vaguest thing they can say about the one machine
  # state worth naming exactly.
  stamp_values='1000 3000 3000 5000'
  COZY_CANARY_MEM_BLOCK_MIB=100
  COZY_CANARY_MEM_BLOCKS=8
  COZY_CANARY_RUN_BOUND=20
  rc=0

  run_canary_here || rc=$?

  capture=$(capture_path)
  [ "$rc" -eq 0 ] || {
    echo "FAIL: two arms stopped at their ceiling reported a shortfall ($rc); the call sites would say no reading was taken for the loudest reading this collector produces" >&2
    cat "$capture" >&2
    return 1
  }
  if [ "${_COZY_CANARY_ALERT:-unset}" != 1 ]; then
    echo "expected two arms stopped at their ceiling to raise the alert, found ${_COZY_CANARY_ALERT:-unset}" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "an arm stopped one tick short of its ceiling is not the ceiling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  # 1099 centiseconds, so 10990 ms against an 11000 ms bound. The bound is not
  # the 20 the default carries: this is the only case that reaches the sentence
  # naming the ceiling it did not reach, so it is the only one that can tell
  # that sentence reading the knob from it reading the compiled-in figure.
  stamp_values='1000 1050 1050 2149'
  COZY_CANARY_MEM_BLOCK_MIB=100
  COZY_CANARY_MEM_BLOCKS=8
  COZY_CANARY_RUN_BOUND=11

  run_canary

  capture=$(capture_path)
  # A ceiling that fired cannot read short of its bound: both stamps truncate
  # hundredths off the one clock and the bound is whole seconds, so the two
  # floors differ by at least the bound. A duration under it was produced by
  # something else, and filing that as the ceiling manufactures the very finding
  # this collector exists to detect.
  assert_file_lacks_pattern 'it was stopped at the 11s ceiling' "$capture"
  assert_file_contains 'before the 11s ceiling could have fired' "$capture"
  rm -rf "$tmp"
}

@test "an arm stopped exactly at its ceiling is the ceiling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  # 1100 centiseconds, so 11000 ms against an 11000 ms bound: the shortest
  # duration a fired ceiling can produce. This case and the one above it hold
  # the comparison exactly where it belongs -- at the bound it is the ceiling,
  # one tick under it is not -- so both a reintroduced tolerance and a
  # comparison tightened to greater-than fail one of the pair. The bound here is
  # deliberately not the 20 the default carries: every other ceiling case sets
  # it to exactly that default, so their assertions would hold just as well if
  # the comparison read the compiled-in figure instead of the knob the caller
  # set. Reading the default here classifies a fired ceiling as something else.
  stamp_values='1000 1050 1050 2150'
  COZY_CANARY_MEM_BLOCK_MIB=100
  COZY_CANARY_MEM_BLOCKS=8
  COZY_CANARY_RUN_BOUND=11

  run_canary

  capture=$(capture_path)
  # The bound on the clock exactly, which is what a ceiling that fired produces
  # at its shortest. Filing this as something else ending the work early would
  # lose the strongest reading the collector takes, so the comparison has to
  # admit the bound itself and nothing under it.
  assert_file_contains 'it was stopped at the 11s ceiling' "$capture"
  assert_file_contains 'BELOW 73 MiB per second' "$capture"
  assert_file_lacks_pattern 'before the 11s ceiling could have fired' "$capture"
  # The kill note belongs to 124's sibling status and must not appear here. The
  # case that pins the note pins only its presence, and every case that reaches
  # this sentence on a 124 asserts it by two substrings the note sits between,
  # so without this line the note could be attached to every ceiling report and
  # nothing would notice -- telling a triager a kill ended an arm the term
  # signal ended, which is the attribution this collector is built to refuse.
  assert_file_lacks_pattern 'a kill ended the arm' "$capture"
  rm -rf "$tmp"
}

@test "an arm that failed on its own account produces no reading and names its exit status" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=127
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary

  capture=$(capture_path)
  # 127 is a binary missing from this machine and a non-zero from the work
  # itself is something else. The number is what tells them apart, so it goes in
  # the line rather than only into the stderr above it. Said as what the arm
  # ended on rather than as what the work returned, because a ceiling that fires
  # ends the arm with timeout's status while work that ends by itself keeps its
  # own, and the number does not say which happened.
  assert_file_contains 'it ended 127' "$capture"
  # The sentence says "the duration above", which is a claim about order rather
  # than about presence: every no-reading sentence points back at the elapsed
  # line instead of repeating the figure, so the line has to be written before
  # the status is classified. Asserting only that it exists somewhere would hold
  # just as well with it printed underneath.
  elapsed_at=$(grep -n '^elapsed: ' "$capture" | head -n 1 | cut -d: -f1)
  ended_at=$(grep -n 'it ended 127' "$capture" | head -n 1 | cut -d: -f1)
  for v in elapsed_at ended_at; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from the capture; without it this check reports success for having lost its input" >&2
      cat "$capture" >&2
      return 1
    fi
  done
  if [ "$elapsed_at" -ge "$ended_at" ]; then
    echo "the no-reading sentence (capture line $ended_at) says the duration is above it, but the elapsed line is at $elapsed_at" >&2
    cat "$capture" >&2
    return 1
  fi
  # And not as a rate: a duration measured on a command that never ran is how
  # long it took to fail.
  assert_file_lacks_pattern 'MiB per second' "$capture"
  rm -rf "$tmp"
}

@test "an arm that failed instantly is named as failed rather than as too fast to measure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=127
  stage "$tmp"
  stub_stamp
  # Both stamps of the memory arm land on the same tick, which is what a command
  # this machine does not have actually produces: it fails in well under 10ms.
  stamp_values='1000 1050 1050 1050'

  run_canary

  capture=$(capture_path)
  # Read in the other order, the one arm that never ran is reported as the one
  # arm too fast to measure -- and on a machine missing dd that is every run,
  # with the capture claiming the memory arm was instantaneous.
  assert_file_contains 'it ended 127' "$capture"
  assert_file_lacks_pattern 'finished inside one tick' "$capture"
  rm -rf "$tmp"
}

@test "an arm nobody could time is reported as silence rather than as a duration" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # The clock fails on the memory arm's closing stamp.
  stamp_fail_at=4

  run_canary

  capture=$(capture_path)
  assert_file_contains 'the canary clock could not be read at both ends' "$capture"
  # Silence about this machine rather than a finding about it. A missing
  # duration written as a zero, or as the arm having been fast, is the one way
  # this capture could manufacture the conclusion it exists to test for.
  assert_file_lacks_pattern 'MiB per second' "$capture"
  rm -rf "$tmp"
}

@test "an arm whose opening stamp fails reports nothing rather than the machine's uptime" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # The clock fails on the compute arm's OPENING stamp, which the case beside
  # this one never reaches: it fails the closing stamp of the second arm, so
  # indices 1 to 3 go unexercised. The opening stamp is the one that matters,
  # because the start it would have set is initialised to zero: unchecked, the
  # closing stamp turns the machine's whole uptime into the arm's duration, and
  # a duration that large divides to a rate under any floor. The collector would
  # then raise the alert every call site turns into a job-log line -- a
  # pathological finding manufactured on a healthy machine, which is the one
  # outcome this collector must never produce.
  stamp_fail_at=1
  stamp_values='1000 1050 1150 1250'
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=1000
  COZY_CANARY_MEM_BLOCKS=1

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'the canary clock could not be read at both ends' "$capture"
  # The arm beside it was timed and carries the run's only figure.
  assert_file_contains 'rate: 1000 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 0 ]; then
    echo "expected an arm whose opening stamp failed to raise no alert; an alert here is a pathology manufactured out of an unread clock" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "an arm faster than one tick reports no rate rather than dividing by zero" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1000 1000 1000'
  rc=0

  run_canary || rc=$?

  # Dividing by it ends the shell: in dash an arithmetic division by zero is
  # fatal on its own account rather than through errexit, so it fires even
  # inside the `if` condition these call sites use, and on this path that takes
  # the whole diagnostics block with it. The duration is under the resolution
  # rather than measured, and at the sizes this canary runs that is itself a
  # finding.
  [ "$rc" -eq 0 ] || {
    echo "FAIL: a sub-tick duration ended the collector with $rc" >&2
    return 1
  }
  capture=$(capture_path)
  assert_file_contains 'no rate: the arm finished inside one tick' "$capture"
  rm -rf "$tmp"
}

@test "a kill inside one tick is named as a kill rather than as the ceiling or a finish" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  # The memory arm ends with a ceiling-shaped status while both of its stamps
  # land on the same tick, which a real ceiling cannot produce: the ceiling
  # needs the whole bound on the clock. Read by status alone this was reported
  # as the ceiling with no duration, which is a finding about the machine
  # manufactured by whatever actually killed the work.
  stamp_values='1000 1050 1050 1050'
  COZY_CANARY_RUN_BOUND=20
  rc=0

  run_canary || rc=$?

  capture=$(capture_path)
  assert_file_contains 'before the 20s ceiling could have fired' "$capture"
  assert_file_lacks_pattern 'finished inside one tick' "$capture"
  assert_file_lacks_pattern 'stopped at the' "$capture"
  # And the shell survives it: on the failure path an exit here takes the rest
  # of the diagnostics block with it, straight past the call site's guard. The
  # tenant snapshot is armed as an EXIT trap before the block, so it still runs.
  [ "$rc" -eq 0 ] || {
    echo "FAIL: a killed arm beside a healthy one ended the collector with $rc" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "a clock that could not be read yields no reading rather than a silent success" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # No stamp at all resolves, so every arm fails at its clock rather than at its
  # work. The backwards-clock case below pins the same contract for the other
  # way a duration can be refused; this one is the half that was reported only
  # in the capture. A sample that measured nothing has to say so through its
  # status too, because the status is what the two call sites outside the
  # diagnostics block turn into a job-log line -- report it as success and a run
  # that timed nothing at all passes in silence.
  stamp_values=''
  rc=0

  run_canary || rc=$?

  [ "$rc" -ne 0 ] || {
    echo "FAIL: a sample whose clock could not be read at either end reported success" >&2
    return 1
  }
  capture=$(capture_path)
  assert_file_contains 'the canary clock could not be read at both ends' "$capture"
  # The legend states units of its own, so the absence is asserted on the rate
  # line rather than on the words.
  assert_file_lacks_pattern '^rate: ' "$capture"
  rm -rf "$tmp"
}

@test "a clock that went backwards yields no reading rather than a negative duration" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 900 900 800'
  rc=0

  run_canary || rc=$?

  # Neither arm yields a figure, so the collector reports a shortfall -- which
  # is what the two call sites outside the diagnostics block turn into a line in
  # the job log.
  [ "$rc" -ne 0 ] || {
    echo "FAIL: a capture holding no figure at all reported success" >&2
    return 1
  }
  capture=$(capture_path)
  # Named as the instrument being wrong rather than as the clock being
  # unreadable: those send a reader to different places, and one of them is a
  # machine that has nothing wrong with it. Every figure here is derived from
  # the duration, so a negative one produces a negative rate that reads exactly
  # like a real number with a sign in front.
  assert_file_contains 'the second stamp was the earlier one' "$capture"
  assert_file_lacks_pattern 'elapsed: -' "$capture"
  rm -rf "$tmp"
}

@test "a rate under one unit per second is said in words rather than rounded to nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # 8 MiB in twenty seconds is under half a MiB per second, which integer
  # division reports as none at all.
  stamp_values='1000 1050 1050 3050'
  COZY_CANARY_MEM_BLOCK_MIB=2
  COZY_CANARY_MEM_BLOCKS=4

  run_canary

  capture=$(capture_path)
  # `rate: 0` is the reading this capture must never produce: it is the shape of
  # a machine that did nothing, and here it would mean arithmetic that ran out
  # of places on a machine that was merely very slow -- which is the finding, so
  # rounding it away loses exactly the run this collector exists for.
  assert_file_lacks_pattern 'rate: 0 ' "$capture"
  assert_file_contains 'under one MiB per second' "$capture"
  # And the inputs are in the file, so the reader can divide them themselves.
  assert_file_contains 'work: 8 MiB' "$capture"
  assert_file_contains 'elapsed: 20000 ms' "$capture"
  rm -rf "$tmp"
}

@test "what the work said on stderr lands beside the figure it explains" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050'
  # The arm reads the ceiling and its grace from globals the library settles at
  # source time, so driving it on its own needs no setup; they are restated here
  # to pin the two numbers this case's assertions are written against.
  COZY_CANARY_RUN_BOUND=20
  COZY_DIAG_READ_GRACE=5

  # Driven through one arm with a command of the test's own, rather than through
  # the collector: what is pinned is that an arm keeps its stderr, and dd is the
  # arm that relies on it -- it reports there by design, and a `timeout` giving
  # up says so there too.
  capture="$tmp/one-arm.txt"
  ( set +x; _cozy_canary_report_arm "$capture" 'synthetic' 100 units 1 \
    sh -c 'echo "$((6*7))-complained" >&2' )

  # The marker is computed by the work rather than written into its command
  # line, and that is the whole of the case: the reporter echoes the command
  # before running it, so a needle that appears in both is satisfied by the echo
  # and says nothing about where the arm's stderr went.
  assert_file_contains '42-complained' "$capture"
  assert_file_lacks_pattern 'command: .*42-complained' "$capture"
  assert_file_contains 'command: sh -c echo "$((6*7))-complained" >&2' "$capture"
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# What the collector tells its caller.
# ---------------------------------------------------------------------------

@test "the collector reports a shortfall only when the capture holds no figure at all" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  rc=0

  run_canary || rc=$?
  [ "$rc" -eq 0 ] || {
    echo "FAIL: a capture holding two rates reported a shortfall ($rc)" >&2
    return 1
  }

  # One arm failing is not a shortfall: the other still carries a figure, and
  # both call sites outside the diagnostics block turn a non-zero into a warning
  # line saying nothing was measured.
  timeout_rc_match=dd
  timeout_rc_override=127
  stamp_index=0
  rc=0
  run_canary 2 || rc=$?
  [ "$rc" -eq 0 ] || {
    echo "FAIL: one failed arm out of two reported a shortfall ($rc)" >&2
    return 1
  }

  # Both failing is.
  timeout_rc_match=
  stamp_index=0
  rc=0
  run_canary 3 || rc=$?
  [ "$rc" -ne 0 ] || {
    echo "FAIL: a capture holding no figure at all reported success" >&2
    return 1
  }

  # And so is both being killed before the ceiling, which reaches the shortfall
  # by a different branch: a status the ceiling also produces, refused as the
  # ceiling because the clock says the bound had not run out. Three of the four
  # ways an arm can yield no reading are pinned for status elsewhere -- the
  # clock unreadable, the clock backwards, the work failing on its own account
  # -- and this is the fourth. Left unpinned, two arms killed from outside,
  # which is the OOM case the ceiling attribution is written for, would report
  # success with nothing measured and both call sites would stay quiet.
  timeout_rc_override=137
  stamp_values='1000 1200 1200 1400'
  COZY_CANARY_RUN_BOUND=20
  stamp_index=0
  rc=0
  run_canary 4 || rc=$?
  [ "$rc" -ne 0 ] || {
    echo "FAIL: two arms killed before their ceiling reported success with no figure in the capture" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "no path that measured nothing raises the alert" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # The alert is what all three reporting sites turn into a job-log line saying
  # the machine was not getting the work done. Raised on a path that took no
  # reading, it is a pathology manufactured out of nothing -- the one outcome
  # this collector must never produce. The clock-failure path is pinned against
  # it by the case that drives an unreadable stamp; these are the other three
  # ways an arm can end without a figure, each of which reaches a different
  # branch of the reporter.
  COZY_CANARY_RUN_BOUND=20

  # The work failed on its own account.
  timeout_rc_match=
  timeout_rc_override=127
  stamp_values='1000 1200 1200 1400'
  stamp_index=0
  run_canary_here || true
  [ "${_COZY_CANARY_ALERT:-unset}" = 0 ] || {
    echo "expected two arms that failed on their own account to raise no alert, found ${_COZY_CANARY_ALERT:-unset}" >&2
    return 1
  }

  # Killed before the ceiling could have fired.
  timeout_rc_override=137
  stamp_index=0
  run_canary_here 2 || true
  [ "${_COZY_CANARY_ALERT:-unset}" = 0 ] || {
    echo "expected two arms killed before their ceiling to raise no alert, found ${_COZY_CANARY_ALERT:-unset}" >&2
    return 1
  }

  # Finished inside one tick, which is too fast to measure rather than slow.
  timeout_rc_override=
  stamp_values='1000 1000 1000 1000'
  stamp_index=0
  run_canary_here 3 || true
  [ "${_COZY_CANARY_ALERT:-unset}" = 0 ] || {
    echo "expected two arms finishing inside one tick to raise no alert, found ${_COZY_CANARY_ALERT:-unset}" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "each sample announces itself and its number in the job log" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  out=$(run_canary 2)

  # The line is the only trace of the canary a reader of the job log gets on a
  # green run before the report is downloaded, and the sample number is what
  # ties it to the side of the wait it was taken on.
  case "$out" in
    *'--- running the runner fixed-work canary (sample 2) ---'*) ;;
    *)
      echo "FAIL: the job log does not announce the canary sample; it said: $out" >&2
      return 1
      ;;
  esac
  rm -rf "$tmp"
}

@test "the sample number decides which directory a reading lands in" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary 2

  # Sample 1 is the pre-wait reading and sample 2 the post-wait one; a
  # collector that folded them into one directory would overwrite the before
  # with the after, and the pair would compare a reading against itself.
  [ -f "$(capture_path 2)" ] || {
    echo "FAIL: sample 2 did not land in its own directory" >&2
    return 1
  }
  [ ! -e "$(capture_path 1)" ] || {
    echo "FAIL: sample 2 wrote into the sample-1 directory" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "a capture left by an earlier run is truncated rather than appended to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # The report directory outlives a run, so the same sample number can find a
  # file already there. Appended to, the capture would carry two runs of one
  # sample with nothing between them to say where the first ended, and every
  # figure a reader took from it could belong to either. The neighbouring case
  # covers the other half of this line -- that a failed open returns rather than
  # ending the shell -- and cannot see the truncation, because a failed append
  # fails the same way.
  capture=$(capture_path)
  mkdir -p "$(dirname "$capture")"
  printf 'STALE-FROM-AN-EARLIER-RUN\n' >"$capture"

  run_canary

  assert_file_lacks_pattern 'STALE-FROM-AN-EARLIER-RUN' "$capture"
  rm -rf "$tmp"
}

@test "a capture file that cannot be opened reaches the job log rather than ending the shell" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The capture path is made a directory, so the open fails while the mkdir -p
  # above it succeeds -- the one shape where the truncation is the first write
  # to notice, and one that fails for root and non-root alike. It must fail by
  # returning: this truncation used to be spelled `: >`, and `:` is a POSIX
  # special built-in, whose redirection failure exits a non-interactive shell
  # -- dash, the /bin/sh of the sandbox image -- taking the rest of the
  # diagnostics block with it, straight past the call site's `|| true`. The
  # tenant snapshot behind it is an EXIT trap and runs either way; what the exit
  # costs is everything the block had left to collect.
  mkdir -p "$(capture_path)"
  rc=0

  ( set +x; cozy_capture_runner_canary 1 >"$tmp/out" 2>&1 ) || rc=$?

  [ "$rc" -ne 0 ] || {
    echo "FAIL: a collector that could write nothing reported success" >&2
    return 1
  }
  assert_file_contains 'could not be opened for writing' "$tmp/out"
  rm -rf "$tmp"
}

@test "an alert from the sample before does not survive a sample that returned early" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # The flag is cleared at the top of the collector rather than beside the arms,
  # so that it is cleared on the paths that return before any arm runs too. The
  # case that pins it beside the arms cannot see this one: it drives a sample
  # that reaches its work, and the early returns are exactly the paths that do
  # not. Left standing, sample 1's alert is read back after a sample 2 that
  # measured nothing, and the call site prints a pathology naming a sample that
  # took no reading.
  _COZY_CANARY_ALERT=1
  printf 'not a directory\n' >"$tmp/blocked"
  COZY_REPORT_DIR="$tmp/blocked"
  rc=0

  # Run in this shell rather than a subshell: the flag the call sites branch on
  # would go with the subshell.
  set +x
  cozy_capture_runner_canary 2 >"$tmp/out" 2>&1 || rc=$?

  [ "${_COZY_CANARY_ALERT:-unset}" = 0 ] || {
    echo "expected a sample that returned before its arms to clear the alert, found ${_COZY_CANARY_ALERT:-unset}; the flag would be read back as a pathology for a sample that measured nothing" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "a report directory that cannot be created reaches the job log" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # A file where the directory tree has to go, so mkdir -p cannot succeed.
  printf 'not a directory\n' >"$tmp/blocked"
  COZY_REPORT_DIR="$tmp/blocked"
  rc=0

  ( set +x; cozy_capture_runner_canary 1 >"$tmp/out" 2>&1 ) || rc=$?

  # With no directory every write below it fails, the collector becomes a silent
  # no-op, and the artifact carries exactly the empty space it exists to refuse
  # -- with nowhere to put a marker saying so, which is why this one goes to the
  # log and not to the report.
  [ "$rc" -ne 0 ] || {
    echo "FAIL: a collector that could write nothing reported success" >&2
    return 1
  }
  assert_file_contains 'could not be created' "$tmp/out"
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# The legends, which are what let a single red run be read at all.
# ---------------------------------------------------------------------------

@test "every legend line lands as one line rather than shredded into words" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary

  # Each legend is one long single-quoted shell string, and an apostrophe inside
  # one ends the quote: the sentence then reaches the file as one word per line,
  # every `[` unmatched, and the capture still passes any assertion that greps
  # for a short phrase. This checks the shape instead -- a bracketed line opens
  # and closes on the same line -- because that is what a shredded legend loses.
  capture=$(capture_path)
  if awk '/^\[/ && $0 !~ /\]$/ { print FILENAME ": " $0; found = 1 } END { exit found ? 1 : 0 }' "$capture"; then
    :
  else
    echo "FAIL: a bracketed legend line does not close on its own line, which is what an apostrophe inside its single quotes does to it" >&2
    return 1
  fi
  grep -q '^\[' "$capture" || {
    echo "FAIL: the capture carries no bracketed legend line at all" >&2
    return 1
  }
  rm -rf "$tmp"
}

@test "the expected ranges quote the sizes this run actually used" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  COZY_CANARY_CPU_ITERATIONS=777
  # The floors are overridden away from their defaults for the reason the
  # ceiling case sets a bound of 11: the legend interpolates them, so a fixture
  # left on the default cannot tell a templated figure from one written into the
  # sentence.
  COZY_CANARY_MEM_MIN_RATE=17
  COZY_CANARY_CPU_MIN_RATE=19
  COZY_CANARY_MEM_BLOCK_MIB=3
  COZY_CANARY_MEM_BLOCKS=5

  run_canary

  # The whole worth of this capture is that its number can be called
  # pathological with no green run beside it, and that rests on the stated range
  # describing the work that was actually done. Restated rather than derived,
  # the range survives a change to the sizes and then describes a different
  # experiment, which is worse than no range at all.
  capture=$(capture_path)
  assert_file_contains 'the 15 MiB it writes' "$capture"
  assert_file_contains 'the 777 of them here' "$capture"
  # The sizes are data and the cache figure is context: the sentence must not
  # assert which of the two is larger, because the relation is a property of
  # the constants at their declaration, not of this template.
  assert_file_contains '3 MiB here, against the 32 MiB' "$capture"
  # The floors the alert compares against are printed from the same constants
  # rather than written into the sentence, so what a reader is told and what the
  # collector decides on cannot drift apart.
  assert_file_contains "anything under ${COZY_CANARY_MEM_MIN_RATE} in the MiB-per-second unit" "$capture"
  assert_file_contains "anything under ${COZY_CANARY_CPU_MIN_RATE} iterations a second" "$capture"
  rm -rf "$tmp"
}

@test "the capture names what it cannot separate and where that answer lives" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary

  # A slow arm has two readings, and only one of them is this collector's. A
  # capture that stated the interesting one and stayed quiet about the dull one
  # would be read as proof of the interesting one, which is the failure this
  # whole investigation keeps producing.
  capture=$(capture_path)
  assert_file_contains 'It reads wall clock only' "$capture"
  assert_file_contains 'runner-kernel-cpu-time' "$capture"
  assert_file_contains 'sandbox-host-cpu-time' "$capture"
  # And it describes what each of those two actually covers, because the two
  # are nothing alike: the runner-kernel rows bracket the wait on both paths,
  # while the sandbox-host rows are taken inside the on-failure diagnostics
  # block, seconds apart, minutes after the wait failed. A legend equating them
  # sends a reader to interpret a 12-second post-mortem pair as the whole join
  # window, which is why the legend states the window each of them covers
  # instead of naming the two side by side.
  assert_file_contains 'a pair bracketing the node-join wait' "$capture"
  assert_file_contains 'a pair seconds apart inside the on-failure diagnostics block' "$capture"
  # The legends below are the standalone-readability deliverable, and a reader of
  # a red run with no green one beside it needs each: which layer the reading
  # describes and where the answers a layer down are kept, what a difference
  # between the samples means, that the collector's own burn sits outside the
  # intervals the sibling pairs divide by, and the resolution every figure here
  # is quantised to.
  assert_file_contains 'this canary runs on the RUNNER VM, inside the sandbox container' "$capture"
  assert_file_contains 'sandbox-host/talos-' "$capture"
  assert_file_contains 'A difference between them puts the change inside the interval the pair brackets' "$capture"
  assert_file_contains 'neither burn falls inside any interval those pairs divide by' "$capture"
  assert_file_contains 'quantised to 10ms' "$capture"
  # The working set is what makes the two answer the same interference
  # differently; they differ in plenty else, including how much work each does,
  # so the sentence claims the one property the two-arm design rests on rather
  # than a separation the arms do not have.
  assert_file_contains 'what makes them answer the same interference differently is the working set' "$capture"
  rm -rf "$tmp"
}

@test "the capture records the wall-clock window its arms ran in" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary

  # The durations inside the capture are read from a monotonic clock that
  # says nothing about when, and the two samples sit the whole node-join wait
  # apart; without this window a reader cannot put either sample beside the
  # console capture or anything else stamped in the report.
  capture=$(capture_path)
  grep -Eq '\[read attempted from [0-9]+ to [0-9]+ epoch seconds\]' "$capture" || {
    echo "FAIL: the capture does not record when its arms were attempted" >&2
    cat "$capture" >&2
    return 1
  }
  rm -rf "$tmp"
}

# ---------------------------------------------------------------------------
# Where the two samples sit, which is what decides what they mean.
# ---------------------------------------------------------------------------

@test "the canary samples sit outside the three counter pairs on every path" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The three pairs beside it read running totals, so each divides by whatever
  # runs between its two readings. The canary is the one collector here that
  # occupies a core rather than reading a file, so a sample taken inside any of
  # those brackets inflates the numerator of a rate the pair reports as the
  # node-join window. Outside all three on both sides, it cannot.
  # Anchored on the wait's own body rather than on its shape: `timeout Nm bash -c`
  # matches more than one line in the library, and picking the first would
  # re-anchor this check the day another one is added above it.
  wait_line=$(awk '/^  timeout [0-9]+m bash -c/ { cand = NR; next } cand && NR == cand + 1 && /get nodes --no-headers/ { print cand; exit } { cand = 0 }' "$lib")
  first=$(grep -n 'cozy_capture_runner_canary 1' "$lib" | head -n 1 | cut -d: -f1)
  green=$(awk -v w="$wait_line" 'NR > w && /cozy_capture_runner_canary 2/ { print NR; exit }' "$lib")
  tail_line=$(grep -n '^  versions=\$(kubectl --kubeconfig' "$lib" | head -n 1 | cut -d: -f1)
  console_line=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  # The signal is a separate word from the keyword, the way the fixtures in
  # hack/bats-no-exit-trap.bats assemble theirs: that guard matches the pair
  # lexically, so a pattern naming both would be counted here as an installed
  # trap this file would then have to declare as debt.
  armed_sig=EXIT
  armed_line=$(grep -n "^  trap '_tenant_snapshot_on_fail' ${armed_sig}" "$lib" | head -n 1 | cut -d: -f1)
  red=$(awk -v w="$wait_line" 'NR < w && /cozy_capture_runner_canary 2/ { line = NR } END { if (line) print line }' "$lib")
  pairs_first=''
  pairs_green=''
  pairs_red=''
  # Derived across all three pairs rather than anchored on one of them. Which
  # sibling sits outermost is not a property anything holds -- it is today's
  # source order -- so a guard that compared against one name would keep passing
  # once the three were reordered around it, while the canary's burn sat inside
  # whichever pair moved. What has to hold is the extremes: earlier than the
  # earliest first reading, later than the latest second one, on both paths.
  for fn in cozy_capture_sandbox_kvm_exits cozy_capture_runner_kernel_cpu_time \
    cozy_capture_sandbox_qemu_thread_cpu; do
    sib_first=$(grep -n "${fn} 1" "$lib" | head -n 1 | cut -d: -f1)
    sib_green=$(awk -v w="$wait_line" -v f="${fn} 2" 'NR > w && index($0, f) { print NR; exit }' "$lib")
    sib_red=$(awk -v w="$wait_line" -v f="${fn} 2" 'NR < w && index($0, f) { line = NR } END { if (line) print line }' "$lib")
    for v in sib_first sib_green sib_red; do
      eval "n=\$$v"
      if [ -z "$n" ]; then
        echo "expected to read $v for $fn from $lib; without it this guard reports success for having lost its input" >&2
        return 1
      fi
    done
    if [ -z "$pairs_first" ] || [ "$sib_first" -lt "$pairs_first" ]; then
      pairs_first="$sib_first"
    fi
    if [ -z "$pairs_green" ] || [ "$sib_green" -gt "$pairs_green" ]; then
      pairs_green="$sib_green"
    fi
    if [ -z "$pairs_red" ] || [ "$sib_red" -gt "$pairs_red" ]; then
      pairs_red="$sib_red"
    fi
  done
  for v in wait_line first green tail_line red console_line armed_line \
    pairs_first pairs_green pairs_red; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$first" -ge "$pairs_first" ]; then
    echo "the canary sample before the wait (line $first) is taken after the earliest of the three counter readings (line $pairs_first), so its burn falls inside the interval that pair divides by" >&2
    return 1
  fi
  if [ "$green" -le "$pairs_green" ]; then
    echo "the passing path takes its canary sample (line $green) before the latest of the three second readings (line $pairs_green), so its burn falls inside the interval that pair divides by" >&2
    return 1
  fi
  if [ "$green" -ge "$tail_line" ]; then
    echo "the passing path's canary sample (line $green) is taken after the suite's happy-path tail begins (line $tail_line), so the green figure describes a different window than the red one" >&2
    return 1
  fi
  if [ "$red" -le "$pairs_red" ]; then
    echo "the failure path takes its canary sample (line $red) before the latest of the three second readings (line $pairs_red), so its burn falls inside the interval that pair divides by" >&2
    return 1
  fi
  # And the failure path's sample is bounded from above as well, the way the
  # passing path's already is. The two bounds are not the same requirement: the
  # lower one keeps the burn out of a pair's interval, this one keeps the figure
  # comparable. Everything from the console down can spend the whole diagnostics
  # phase, so a sample that drifted behind it describes a machine minutes past
  # the failure, against a green figure taken seconds after the wait -- and the
  # budget guard prices only what sits above the console, so the drift would
  # also drop this collector out of that sum while it still runs.
  if [ "$red" -ge "$console_line" ]; then
    echo "the failure path takes its canary sample (line $red) at or after the serial console capture (line $console_line), so the red figure describes a machine minutes past the failure and the budget guard stops pricing it" >&2
    return 1
  fi
  # The pre-wait sample is bounded from below for the same reason the others are
  # bounded at all: the legend tells a reader the pair brackets the wait and the
  # readings on either side of it. Taken before the tenant is even up, it would
  # bracket the whole test instead, and the sentence would be false about the
  # artifact it is printed into.
  if [ "$first" -le "$armed_line" ]; then
    echo "the canary sample before the wait (line $first) is taken at or before the tenant snapshot trap is armed (line $armed_line), so it describes the run before the cluster it is meant to characterise exists" >&2
    return 1
  fi
}

@test "both call sites outside the diagnostics block report a shortfall on their own side of the wait" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # docs/agents/e2e-testing.md requires what a passing-path collector could not
  # collect at all to reach the job log, and one of these two runs on exactly
  # that path, where the report is the artifact nobody downloads. Each side is
  # counted through the clause that names it rather than through the shared
  # prefix: which side of the wait a shortfall fell on is the whole product of a
  # pair, and two lines matched by one prefix could be swapped between the sites
  # with the count unchanged.
  # `|| true`: grep -c exits 1 on a count of zero, and under errexit that ends
  # the test before the branch below can say what was missing -- the guard would
  # still fail, with its diagnostic replaced by silence, on exactly the
  # regression it exists to name.
  calls=$(grep -c 'if ! cozy_capture_runner_canary [12]; then' "$lib" || true)
  if [ "$calls" -ne 2 ]; then
    echo "expected both canary samples outside the diagnostics block to consume the collector's status with 'if !', found $calls" >&2
    return 1
  fi
  # Each clause is located rather than counted. A count of one apiece is
  # satisfied by the two lines swapped between the sites, and a shortfall
  # reported on the wrong side of the wait is worse than none: the pair exists to
  # say which side moved.
  # Anchored on the wait's own body rather than on its shape: `timeout Nm bash -c`
  # matches more than one line in the library, and picking the first would
  # re-anchor this check the day another one is added above it.
  wait_line=$(awk '/^  timeout [0-9]+m bash -c/ { cand = NR; next } cand && NR == cand + 1 && /get nodes --no-headers/ { print cand; exit } { cand = 0 }' "$lib")
  before=$(grep -n 'produced no reading before the node-join wait' "$lib" | head -n 1 | cut -d: -f1)
  after=$(grep -n 'produced no reading after the node-join wait' "$lib" | head -n 1 | cut -d: -f1)
  tail_line=$(grep -n '^  versions=\$(kubectl --kubeconfig' "$lib" | head -n 1 | cut -d: -f1)
  for v in wait_line tail_line before after; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$before" -ge "$wait_line" ]; then
    echo "the shortfall line naming the pre-wait sample sits at line $before, at or after the wait at $wait_line" >&2
    return 1
  fi
  if [ "$after" -le "$wait_line" ] || [ "$after" -ge "$tail_line" ]; then
    echo "the shortfall line naming the post-wait sample sits at line $after, outside the stretch between the wait at $wait_line and the suite's happy-path tail at $tail_line" >&2
    return 1
  fi
}

# ---------------------------------------------------------------------------
# What a pathological reading does, which is what makes the collector worth
# taking on the passing path.
# ---------------------------------------------------------------------------

@test "a memory rate under the floor the legend states raises the alert" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # 8 MiB in two seconds: 4 MiB per second, against a floor of 500. The compute
  # arm is sized to 12,000,000 iterations a second, above its own floor, so this
  # arm is the only one that can raise the alert asserted below. The alert is
  # one flag for the sample rather than one per arm, so a compute arm left under
  # its own floor here would satisfy the assertion before this arm ran at all,
  # and the memory floor could then be disconnected entirely with this case
  # still green.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=2
  COZY_CANARY_MEM_BLOCKS=4

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'rate: 4 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 1 ]; then
    echo "expected a memory rate of 4 MiB per second, against the ${COZY_CANARY_MEM_MIN_RATE} the legend calls the floor, to raise the alert the call sites turn into a job-log line" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a core at the bottom of the healthy band raises no alert" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1150'
  # 953 MiB in one second, a gigabyte a second expressed in the unit the rate
  # line is printed in. The floor sits well under that, so this core is slow and
  # nothing more. A floor at the near-1000 MiB/s the legend gives for that
  # bottom would raise the warning here -- on a green run, about a machine its
  # own legend calls healthy. At this rate exactly it would not: the comparison
  # is strict.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=953
  COZY_CANARY_MEM_BLOCKS=1

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'rate: 953 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 0 ]; then
    echo "expected a memory rate of 953 MiB per second to raise no alert against the ${COZY_CANARY_MEM_MIN_RATE} floor" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a memory rate exactly at the floor raises no alert" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # A rate landing on the floor rather than above or below it, which is the one
  # case that tells `-lt` from `-le`. The neighbours put a rate on either side
  # and both stay correct under either operator, so without this case the
  # comparison the alert turns on is unconstrained. The block size is derived
  # from the floor rather than written next to it: two seconds of a block twice
  # the floor divides to the floor exactly, whatever the floor is later set to.
  # The compute arm is sized well above its own floor, so the flag read below
  # can only have come from the memory arm.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=$(( COZY_CANARY_MEM_MIN_RATE * 2 ))
  COZY_CANARY_MEM_BLOCKS=1

  run_canary_here

  capture=$(capture_path)
  assert_file_contains "rate: ${COZY_CANARY_MEM_MIN_RATE} MiB per second" "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 0 ]; then
    echo "expected a memory rate of exactly the ${COZY_CANARY_MEM_MIN_RATE} floor to raise no alert: the comparison is strict, and an alert here means it has stopped being" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a memory rate just under the floor raises the alert, not only one far under it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1150'
  # 400 MiB in one second, just under the floor rather than an order of
  # magnitude beneath it. The case above pins where the alert stays quiet and
  # this one pins where it starts, so between them the floor is the number that
  # decides -- a floor moved down far enough to be unreachable leaves the first
  # case green and fails here.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=400
  COZY_CANARY_MEM_BLOCKS=1

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'rate: 400 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 1 ]; then
    echo "expected a memory rate of 400 MiB per second, under the ${COZY_CANARY_MEM_MIN_RATE} floor, to raise the alert the call sites turn into a job-log line" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a compute rate under the floor the legend states raises the alert" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # 1000 iterations in half a second: 2000 a second against a floor of three
  # million. The memory arm beside it is above its own, so each arm's floor is
  # pinned by the case where only that arm is under it -- one floor left at zero
  # would otherwise be covered by the other one firing.
  COZY_CANARY_CPU_ITERATIONS=1000
  COZY_CANARY_MEM_BLOCK_MIB=2048
  COZY_CANARY_MEM_BLOCKS=4

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'rate: 2000 iterations per second' "$capture"
  assert_file_contains 'rate: 4096 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 1 ]; then
    echo "expected a compute rate of 2000 iterations per second, against the ${COZY_CANARY_CPU_MIN_RATE} the legend calls the floor, to raise the alert the call sites turn into a job-log line" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "an arm stopped at its ceiling raises the alert without a rate to compare" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  timeout_rc_match=dd
  timeout_rc_override=124
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 3050'
  # Sized so the bound rounds up to 1639 MiB per second, above the floor, and
  # the compute arm beside it is above its own: the ceiling has to raise the
  # alert on its own account or nothing here does. An arm the bound stopped got
  # less work done than the figure printed for it, which is why that figure is a
  # bound rather than a rate -- and why a machine whose ceiling is reached at a
  # size the floor would have passed must not read as healthy.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=2048
  COZY_CANARY_MEM_BLOCKS=16
  COZY_CANARY_RUN_BOUND=20

  run_canary_here

  capture=$(capture_path)
  assert_file_contains 'it was stopped at the 20s ceiling' "$capture"
  # Stated so the case cannot quietly stop being about the ceiling: a size whose
  # bound falls under the floor would satisfy the assertion below through the
  # floor instead, and the ceiling could then be dropped from the alert with
  # every test here still green.
  assert_file_contains 'BELOW 1639 MiB per second' "$capture"
  if [ "${_COZY_CANARY_ALERT:-unset}" != 1 ]; then
    echo "expected an arm stopped at its ceiling, at a size whose bound sits above the floor, to raise the alert the call sites turn into a job-log line" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "an alert raised before the wait does not stand as the reading taken after it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  # Eight stamps: four for each sample, because run_kubernetes_test takes both
  # from one shell and the alert is one variable for the pair.
  stamp_values='1000 1050 1050 1250 1250 1300 1300 1500'
  COZY_CANARY_CPU_ITERATIONS=1000
  COZY_CANARY_MEM_BLOCK_MIB=2048
  COZY_CANARY_MEM_BLOCKS=4

  run_canary_here 1
  first="${_COZY_CANARY_ALERT:-unset}"
  COZY_CANARY_CPU_ITERATIONS=6000000
  run_canary_here 2
  second="${_COZY_CANARY_ALERT:-unset}"

  if [ "$first" != 1 ]; then
    echo "expected sample 1, whose compute arm ran at 2000 iterations a second, to raise the alert; got $first" >&2
    return 1
  fi
  # The whole product of the pair is which side of the wait a change fell on. An
  # alert left standing from the sample before it would be published as the
  # reading taken after it, so a run that went into the wait slow and came out
  # healthy would be reported as one that never recovered -- a warning on a green
  # run, which is the kind a reader learns to skip.
  if [ "$second" != 0 ]; then
    echo "expected sample 2, with both arms above their floors, to leave no alert standing from sample 1; got $second" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a reading inside the range the legend states raises nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'
  # Both arms above their floors. Without this case an alert wired to fire
  # always would satisfy every assertion above it, and the job log would carry
  # the warning on every green run until a reader learned to skip it.
  COZY_CANARY_CPU_ITERATIONS=6000000
  COZY_CANARY_MEM_BLOCK_MIB=2048
  COZY_CANARY_MEM_BLOCKS=4

  run_canary_here

  if [ "${_COZY_CANARY_ALERT:-unset}" != 0 ]; then
    echo "expected a compute rate of 12000000 and a memory rate of 4096, both above their floors, to raise no alert" >&2
    cat "$(capture_path)" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the source keeps an alert branch at every reporting site, each naming where it was taken" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # A reading the legend calls pathological is the loudest thing this collector
  # produces, and the report holding it is the artifact nobody downloads on the
  # passing path and the one a triager opens last on the failing one. So all
  # three reporting sites put it in the job log, each naming where it was taken:
  # two outside the diagnostics block, on either side of the wait, and the third
  # inside it, where sample 1's line is a 29m node-join wait above.
  #
  # Counted through those clauses rather than through the shared prefix, for the
  # reason the shortfall guard above gives: three lines matched by one prefix
  # could be swapped between the sites with the count unchanged.
  # `|| true` on each count: grep -c exits 1 on a count of zero, and under
  # errexit that ends the test before the branch below can say what was
  # missing -- the guard would still fail, with its diagnostic replaced by
  # silence, on exactly the regression it exists to name.
  branches=$(grep -cF 'elif [ "${_COZY_CANARY_ALERT:-0}" -eq 1 ]; then' "$lib" || true)
  # Two leading spaces, so an `elif` line does not answer for the plain `if`
  # this counts: the shorter needle is a substring of every one of them.
  in_block=$(grep -cF '  if [ "${_COZY_CANARY_ALERT:-0}" -eq 1 ]; then' "$lib" || true)
  if [ "$branches" -ne 2 ]; then
    echo "expected both samples outside the diagnostics block to branch on the alert with elif, found $branches" >&2
    return 1
  fi
  if [ "$in_block" -ne 1 ]; then
    echo "expected the sample inside the diagnostics block to branch on the alert on its own, found $in_block" >&2
    return 1
  fi
  # Located rather than counted, for the reason the shortfall guard above gives:
  # one line apiece is satisfied by the three of them swapped around.
  fail_fn=$(grep -n '^cozy_report_node_join_failure() {' "$lib" | head -n 1 | cut -d: -f1)
  test_fn=$(grep -n '^run_kubernetes_test() {' "$lib" | head -n 1 | cut -d: -f1)
  # Anchored on the wait's own body rather than on its shape: `timeout Nm bash -c`
  # matches more than one line in the library, and picking the first would
  # re-anchor this check the day another one is added above it.
  wait_line=$(awk '/^  timeout [0-9]+m bash -c/ { cand = NR; next } cand && NR == cand + 1 && /get nodes --no-headers/ { print cand; exit } { cand = 0 }' "$lib")
  before=$(grep -n '» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy before the node-join wait' "$lib" | head -n 1 | cut -d: -f1)
  after=$(grep -n '» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy after the node-join wait' "$lib" | head -n 1 | cut -d: -f1)
  failing=$(grep -n '» WARNING: the runner fixed-work canary did not read inside the range its own legend calls healthy on the failing run' "$lib" | head -n 1 | cut -d: -f1)
  tail_line=$(grep -n '^  versions=\$(kubectl --kubeconfig' "$lib" | head -n 1 | cut -d: -f1)
  for v in fail_fn test_fn wait_line tail_line before after failing; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$failing" -le "$fail_fn" ] || [ "$failing" -ge "$test_fn" ]; then
    echo "the alert line naming the failing run sits at line $failing, outside the diagnostics block between $fail_fn and $test_fn" >&2
    return 1
  fi
  if [ "$before" -le "$test_fn" ] || [ "$before" -ge "$wait_line" ]; then
    echo "the alert line naming the pre-wait sample sits at line $before, not between the test body at $test_fn and the wait at $wait_line" >&2
    return 1
  fi
  if [ "$after" -le "$wait_line" ] || [ "$after" -ge "$tail_line" ]; then
    echo "the alert line naming the post-wait sample sits at line $after, outside the stretch between the wait at $wait_line and the suite's happy-path tail at $tail_line" >&2
    return 1
  fi
}

@test "each arm's floor sits inside the window a bounded arm can reach" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # A floor the ceiling gets to first never fires. The arm is cut off before its
  # rate can fall that far, so every alert arrives as the ceiling and carries a
  # bound rather than the slowdown the floor was meant to name -- the collector
  # goes quiet on exactly the effect it was sized for while its legend states a
  # figure that decides nothing. A floor crossed in the first few ticks is the
  # other end of the same failure: it fires on a duration this clock barely
  # resolves. Derived from the constants because the size, the floor and the
  # ceiling are three numbers in three places, and moving any one of them moves
  # this window.
  iters=$(grep -oE '^COZY_CANARY_CPU_ITERATIONS=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  cpu_floor=$(grep -oE '^COZY_CANARY_CPU_MIN_RATE=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  blk=$(grep -oE '^COZY_CANARY_MEM_BLOCK_MIB=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  blocks=$(grep -oE '^COZY_CANARY_MEM_BLOCKS=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  mem_floor=$(grep -oE '^COZY_CANARY_MEM_MIN_RATE=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  bound=$(grep -oE '^COZY_CANARY_RUN_BOUND_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  for v in iters cpu_floor blk blocks mem_floor bound; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  # Checked before dividing, for the reason the emptiness check above is
  # checked: a floor read out as zero makes the shell die on the division rather
  # than reach the branch below, and the failure then arrives as a raw
  # arithmetic error rather than as a sentence naming the floor and the file it
  # was read from.
  for v in cpu_floor mem_floor; do
    eval "n=\$$v"
    if [ "$n" -eq 0 ]; then
      echo "read $v as zero from $lib; a floor of zero has no window to sit inside and this guard cannot divide by it" >&2
      return 1
    fi
  done
  for arm in "compute:$(( iters * 1000 / cpu_floor ))" \
    "memory:$(( blk * blocks * 1000 / mem_floor ))"; do
    name=${arm%%:*}
    ms=${arm#*:}
    if [ "$ms" -ge $(( bound * 1000 )) ]; then
      echo "the $name arm crosses its floor at ${ms}ms, at or past the ${bound}s ceiling, so the ceiling stops it first and the floor can never fire" >&2
      return 1
    fi
    if [ "$ms" -lt 1000 ]; then
      echo "the $name arm crosses its floor at ${ms}ms, under a hundred ticks of the 10ms clock, so the floor fires on a duration barely above the resolution" >&2
      return 1
    fi
  done
}

@test "the memory block is larger than the cache figure its legend states" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The memory arm measures the memory controller only while its block exceeds
  # what one core can keep cached; a block that fits reads cache bandwidth
  # instead, several times higher, and the two arms stop telling the causes
  # apart. The window guard beside this one divides by the product of the block
  # size and the block count, so the split between them is invisible to it: 8
  # MiB across 1024 blocks writes the same 8192 MiB and passes there. Both
  # numbers are read from the source rather than written here, so the legend and
  # the constant it describes cannot drift apart.
  blk=$(grep -oE '^COZY_CANARY_MEM_BLOCK_MIB=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  cache=$(grep -oE 'against the [0-9]+ MiB of last-level cache' "$lib" | head -n 1 | grep -oE '[0-9]+')
  for v in blk cache; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$blk" -le "$cache" ]; then
    echo "the memory block is ${blk} MiB against the ${cache} MiB of cache the legend states, so the arm fits in cache and stops measuring the memory controller" >&2
    return 1
  fi
  # Bounded above as well: the block is one buffer dd allocates, and the source
  # names the OOM killer taking that buffer as the realistic outside kill. A
  # block large enough to be that buffer would pass the window guard beside this
  # one, which only ever divides by the product of the size and the count.
  if [ "$blk" -gt $(( cache * 8 )) ]; then
    echo "the memory block is ${blk} MiB, more than eight times the ${cache} MiB of cache the legend states, so the arm asks dd for a buffer large enough to be the OOM the ceiling attribution is written for" >&2
    return 1
  fi
}

@test "each arm is sized to the duration its own legend states" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The sizes are chosen so each arm takes about a second, which is what makes
  # the 10ms clock about a percent of the figure rather than a tenth of it. The
  # window guard beside this one divides the quantity by the floor, so it holds a
  # RATIO and moves with the floor: it admits a compute arm a tenth of this size,
  # whose legend would then print "land in about a second" about a run that took
  # a hundred milliseconds. What has to hold is the absolute duration, and both
  # arms carry an anchor for it in the legend already: the compute floor is
  # stated as a factor of ten under a healthy rate, and the memory arm is stated
  # to take about eight seconds at the bottom of its band. Derived rather than
  # timed, because a duration asserted against the clock of whatever machine
  # runs this would be a flake rather than a guard.
  iters=$(grep -oE '^COZY_CANARY_CPU_ITERATIONS=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  cpu_floor=$(grep -oE '^COZY_CANARY_CPU_MIN_RATE=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  blk=$(grep -oE '^COZY_CANARY_MEM_BLOCK_MIB=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  blocks=$(grep -oE '^COZY_CANARY_MEM_BLOCKS=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  mem_floor=$(grep -oE '^COZY_CANARY_MEM_MIN_RATE=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  for v in iters cpu_floor blk blocks mem_floor; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  # Compute: a healthy rate is the floor times the decade the legend states, so
  # the arm's own duration at that rate is what the sentence promises.
  compute_ms=$(( iters * 1000 / ( cpu_floor * 10 ) ))
  if [ "$compute_ms" -lt 500 ] || [ "$compute_ms" -gt 2000 ]; then
    echo "the compute arm runs ${compute_ms}ms at the healthy rate its own legend states, not the about-a-second the sizing comment promises, so the 10ms clock is no longer about a percent of the figure" >&2
    return 1
  fi
  # Memory: the bottom of the band is the size over the eight seconds the legend
  # gives it, and that bottom has to stay above the floor -- below it, a core at
  # the bottom of the healthy band would read as pathological.
  mem_mib=$(( blk * blocks ))
  if [ $(( mem_mib / 8 )) -le "$mem_floor" ]; then
    echo "the memory arm is ${mem_mib} MiB, so the bottom of the band its legend describes is $(( mem_mib / 8 )) MiB per second, at or under the ${mem_floor} floor: a core at the bottom of the healthy band would read as pathological" >&2
    return 1
  fi
}

@test "each arm runs the work its rate is a rate of" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  timeout_calls="$tmp/timeout.calls"
  timeout_skip_command=1
  stage "$tmp"
  stub_stamp
  stamp_values='1000 1050 1050 1250'

  run_canary

  capture=$(capture_path)
  # Every figure in this capture is a rate of THIS work, and nothing else here
  # reads the commands. An awk program with its loop gone and a dd reading a
  # file that yields nothing still take a duration and still divide to a rate,
  # and that rate is process startup reported as the speed of a machine.
  assert_file_contains "for (i = 0; i < ${COZY_CANARY_CPU_ITERATIONS}; i++)" "$capture"
  # Kept for the reason the source states beside it: an awk able to see that the
  # loop has no effect is free to skip it.
  assert_file_contains 'print s }' "$capture"
  # The zero device rather than any readable file: dd's read is what fills the
  # buffer this arm is timing the stores of.
  assert_file_contains 'dd if=/dev/zero of=/dev/null' "$capture"
  rm -rf "$tmp"
}

@test "both kubernetes suites describe the collectors that bracket the wait" {
  # The node-join guard's own failure message sends a reader to both suite files,
  # and the guard that orders those files against the library covers the gated
  # list only -- these four are exempt from it, so nothing else checks the
  # paragraph that names them. A paragraph that went back to describing three
  # collectors would be false in two files and green everywhere.
  for suite in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
    hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
  # `|| true` on each count: grep -c exits 1 on a count of zero, and under
  # errexit that ends the test before the branch below can say what was
  # missing -- the guard would still fail, with its diagnostic replaced by
  # silence, on exactly the regression it exists to name.
    # Counted inside the enumeration rather than anywhere in the file: the
    # paragraph is what names all four, so a canary dropped from it and
    # mentioned elsewhere would satisfy a file-wide count while the sentence
    # stopped identifying the collectors it claims to.
    named=$(sed -n '/The four collectors that bracket the node-join wait/,/sit outside this order/p' "$suite" | grep -cF 'runner fixed-work canary' || true)
    counted=$(grep -cF 'The four collectors that bracket the node-join wait' "$suite" || true)
    if [ "$named" -lt 1 ]; then
      echo "expected $suite to name the runner fixed-work canary among the collectors that bracket the wait, found $named" >&2
      return 1
    fi
    if [ "$counted" -ne 1 ]; then
      echo "expected $suite to describe four collectors bracketing the wait, found $counted" >&2
      return 1
    fi
  done
}
