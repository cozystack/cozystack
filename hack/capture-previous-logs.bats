#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the pure selection helpers in
# hack/e2e-capture-previous-logs.sh -- the logic that decides WHICH restarted
# containers get their previous instance dumped, and in what order:
#
#   - prevlog_filter_restarted -- keep only containers with restartCount > 0;
#   - prevlog_prioritize       -- put the failing test's namespace first;
#   - prevlog_cap              -- bound the dump so a wedged cluster cannot
#                                 explode the catch;
#   - prevlog_logfile_name     -- per-container artifact filename.
#
# The `kubectl logs --previous` capture itself is not unit-testable (it needs a
# live cluster with a crash-looping pod); these tests pin the derivations that
# decide what is worth asking for at all. The restart filter is the one that
# matters most: without it every healthy container would be asked for a
# previous instance that does not exist, turning the catch into a page of noise.
#
# Strategy: the script is sourced once with E2E_CAPTURE_PREVLOGS_LIB set, which
# the script's sourcing guard honours by defining the helpers and returning
# before it touches $1 or runs any capture -- so no cluster is required. Each
# @test then calls the helpers directly and asserts with `[ ... ]`, matching this
# repo's plain-shell bats convention (no `run` helper). The whole-script tests at
# the end of this file work the other way round: they EXECUTE the script against
# a stub kubectl, because what they pin is the artifact it leaves behind.
#
# Title syntax constraints (inherited from cozytest.sh's awk parser):
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name, so keep
#     titles distinctive in their alphanumeric run.
#
# Run with: hack/cozytest.sh hack/capture-previous-logs.bats
#           (or `bats hack/capture-previous-logs.bats` if the bats binary is
#           installed; cozytest.sh is the CI path.)
# -----------------------------------------------------------------------------

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SCRIPT="$HACK_DIR/e2e-capture-previous-logs.sh"

# Load the pure helpers. The guard returns before the capture body, so this is
# side-effect-free and needs no cluster.
E2E_CAPTURE_PREVLOGS_LIB=1
# shellcheck source=/dev/null
. "$SCRIPT"

@test "filter restarted keeps only containers whose restart count is above zero" {
  rows="$(printf '%s\n' \
    'tenant-test|mariadb-test-0|mariadb|container|3' \
    'tenant-test|mariadb-test-1|mariadb|container|0' \
    'cozy-system|healthy|app|container|0' \
    'tenant-test|mariadb-test-0|init-datadir|init|2')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|mariadb-test-0|mariadb|container|3$'
  printf '%s\n' "$out" | grep -q '^tenant-test|mariadb-test-0|init-datadir|init|2$'
  # `! cmd` is vacuous under cozytest's `set -e` (errexit is suppressed for a
  # `!`-negated pipeline), so a filter regression that let these rows through
  # would not fail the test. Assert the absence via `if cmd; then ...; false`.
  if printf '%s\n' "$out" | grep -q 'mariadb-test-1'; then echo "FAIL: must drop the zero-restart replica"; false; fi
  if printf '%s\n' "$out" | grep -q 'cozy-system'; then echo "FAIL: must drop the zero-restart system pod"; false; fi
}

@test "filter restarted drops rows with a malformed or empty restart count" {
  # A non-numeric count must be dropped rather than compared. The row would be
  # skipped either way (`[ "<none>" -gt 0 ]` exits 2, which the filter's
  # `|| continue` catches), so what this pins is the contract -- malformed rows
  # never reach the capture -- not a crash.
  rows="$(printf '%s\n' \
    'tenant-test|pod-a|app|container|' \
    'tenant-test|pod-b|app|container|<none>' \
    'tenant-test|pod-c|app|container|-1' \
    'tenant-test|pod-d|app|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|pod-d|'
}

@test "filter restarted drops rows missing a namespace pod or container field" {
  rows="$(printf '%s\n' \
    '|pod-a|app|container|4' \
    'tenant-test||app|container|4' \
    'tenant-test|pod-c||container|4' \
    'tenant-test|pod-d|app|container|4')"

  out="$(printf '%s\n' "$rows" | prevlog_filter_restarted)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 1 ]
  printf '%s\n' "$out" | grep -q '^tenant-test|pod-d|'
}

@test "prioritize puts the failing test namespace ahead of everything else" {
  rows="$(printf '%s\n' \
    'cozy-linstor|satellite-x|linstor|container|5' \
    'tenant-test|mariadb-test-0|mariadb|container|3' \
    'cozy-cilium|agent-y|cilium|container|2' \
    'tenant-test|mariadb-test-1|mariadb|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize tenant-test)"

  # All four survive -- prioritisation reorders, it never drops.
  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 4 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "mariadb-test-0" ]
  [ "$(printf '%s\n' "$out" | sed -n '2p' | cut -d'|' -f2)" = "mariadb-test-1" ]
  # Non-matching rows keep their relative input order behind the matches.
  [ "$(printf '%s\n' "$out" | sed -n '3p' | cut -d'|' -f2)" = "satellite-x" ]
  [ "$(printf '%s\n' "$out" | sed -n '4p' | cut -d'|' -f2)" = "agent-y" ]
}

@test "prioritize with an empty namespace passes every row through unchanged" {
  rows="$(printf '%s\n' \
    'cozy-linstor|satellite-x|linstor|container|5' \
    'tenant-test|mariadb-test-0|mariadb|container|3')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize '')"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "satellite-x" ]
}

@test "prioritize treats the namespace as text and not as a pattern" {
  # The namespace arrives from the caller, so a `.` in it must not match a namespace
  # it does not name.
  rows="$(printf '%s\n' \
    'tenantXtest|pod-regex|app|container|1' \
    'tenant.test|pod-literal|app|container|1' \
    'cozy-system|pod-other|app|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize 'tenant.test')"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 3 ]
  first=$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)
  if [ "$first" != "pod-literal" ]; then
    echo "FAIL: expected pod-literal first, got '$first' (namespace matched as a pattern)"
    false
  fi
}

@test "prioritize matches the namespace field only and not a pod name substring" {
  # The match must anchor on the LEADING namespace field. The first row is the
  # fixture that makes this test bite: a pod named exactly like the namespace,
  # in a different namespace, so the row contains `tenant-test|` but does not
  # start with it. Drop the `^` from prevlog_prioritize and that row is promoted
  # ahead of the genuine namespace match below, flipping the first two
  # assertions. (A pod merely named `tenant-test-runner` does NOT distinguish
  # the two -- it lacks the trailing `|` and so matches neither form -- which is
  # why it is kept only as the third row rather than carrying the test.)
  # The second row is the other half of the anchor: a SIBLING NAMESPACE sharing
  # the prefix, which nested tenants make routine. It pins the trailing `|` --
  # drop that from the pattern and `tenant-test-child` is treated as the failing
  # test's own namespace and promoted, which is how the cap would get spent on
  # an unrelated tenant's restarts.
  rows="$(printf '%s\n' \
    'cozy-system|tenant-test|app|container|1' \
    'tenant-test-child|neighbour|app|container|1' \
    'cozy-system|tenant-test-runner|app|container|1' \
    'tenant-test|real|app|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_prioritize tenant-test)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 4 ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f1)" = "tenant-test" ]
  [ "$(printf '%s\n' "$out" | sed -n '1p' | cut -d'|' -f2)" = "real" ]
  # Everything else keeps input order behind the single genuine match.
  [ "$(printf '%s\n' "$out" | sed -n '2p' | cut -d'|' -f2)" = "tenant-test" ]
  [ "$(printf '%s\n' "$out" | sed -n '3p' | cut -d'|' -f2)" = "neighbour" ]
  [ "$(printf '%s\n' "$out" | sed -n '4p' | cut -d'|' -f2)" = "tenant-test-runner" ]
}

@test "cap keeps at most the requested number of rows" {
  rows="$(printf '%s\n' \
    'ns|p1|c|container|1' 'ns|p2|c|container|1' 'ns|p3|c|container|1' \
    'ns|p4|c|container|1' 'ns|p5|c|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_cap 3)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 3 ]
  printf '%s\n' "$out" | grep -q '^ns|p1|'
  if printf '%s\n' "$out" | grep -q '^ns|p4|'; then echo "FAIL: cap must drop rows past the limit"; false; fi
}

@test "cap leaves a shorter input untouched" {
  rows="$(printf '%s\n' 'ns|p1|c|container|1' 'ns|p2|c|container|1')"

  out="$(printf '%s\n' "$rows" | prevlog_cap 12)"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 2 ]
}

@test "cap falls back to the default limit when given a malformed value" {
  # COZY_PREVLOG_MAX is operator-supplied, so a typo must degrade to the default
  # rather than make `head -n` fail and drop the whole capture.
  rows="$(i=1; while [ "$i" -le 20 ]; do printf 'ns|p%s|c|container|1\n' "$i"; i=$((i + 1)); done)"
  [ "$(printf '%s\n' "$rows" | grep -c .)" -eq 20 ]

  out="$(printf '%s\n' "$rows" | prevlog_cap 'twelve')"

  [ "$(printf '%s\n' "$out" | grep -c .)" -eq 12 ]
}

@test "logfile name joins namespace pod and container with underscores" {
  name="$(prevlog_logfile_name tenant-test mariadb-test-0 mariadb)"

  [ "$name" = "tenant-test_mariadb-test-0_mariadb.log" ]
}

# --------------------------------------------------------------------------- #
# Whole-script runs against a stub kubectl. Everything above pins a pure        #
# helper; these pin the artifact the script leaves behind, which is what a      #
# reader of the uploaded report actually gets. The script's own explanations of #
# why a capture is short -- the cap, an unreachable API -- used to exist only on #
# the job log's stdout, so the report carried N dumps and no way to tell N from #
# "all of them". Each run below asserts the explanation is IN the output        #
# directory.                                                                    #
#                                                                               #
# The stub answers `kubectl get` from PREVLOG_STUB_ROWS (or fails when          #
# PREVLOG_STUB_LIST_FAIL is set) and `kubectl logs` with one line, so no cluster #
# is involved.                                                                  #
# --------------------------------------------------------------------------- #

prevlog_stub_dir() {
  _sd="$1"
  cat > "$_sd/kubectl" <<'STUB'
#!/bin/sh
case "$1" in
  get)  if [ -n "${PREVLOG_STUB_LIST_FAIL:-}" ]; then
          # A failing cluster-wide list is preceded by one klog discovery-retry
          # line per attempt, each long enough to survive a 200-character trim.
          i=1
          while [ "$i" -le 3 ]; do
            echo 'E0728 11:51:48.306797   82827 memcache.go:265] "Unhandled Error" err="couldn'"'"'t get current server API group list: Get \"https://192.0.2.1:6443/api?timeout=32s\": dial tcp: lookup timed out after some considerable number of characters"' >&2
            i=$((i + 1))
          done
          echo 'Error from server (Forbidden): pods is forbidden' >&2
          exit 1
        fi
        printf '%s\n' "${PREVLOG_STUB_ROWS:-}" ;;
  logs)
    case "${PREVLOG_STUB_LOG_MODE:-}" in
      # Killed mid-line, which is the normal shape of a SIGKILLed stream: the last
      # line has no terminating newline. The script keeps such a capture, so any
      # note appended to it must start a line of its own.
      partial) printf 'first line\nthe decisive partial line'; exit 124 ;;
      # Partial output followed by a NON-timeout failure. kubectl can write some of
      # the stream and then die on its own terms -- a connection reset mid-read --
      # with no clock having run out, so a marker naming the timeout would put a
      # cause in the artifact that was never observed.
      # A refusal with a message, used to prove the reason is lost only when the
      # scratch file cannot be created -- not because the stub stayed quiet.
      # Exits 0, writes NO log, and still emits a warning. The shape where dropping
      # the message costs most: the script would otherwise report "produced no
      # output", a claim about the container made while kubectl was speaking.
      warned_empty)
        echo 'Warning: results are partial, 3 of 7 shards answered' >&2
        exit 0 ;;
      norefusal)
        echo 'Error from server (BadRequest): previous terminated container "postgres" not found' >&2
        exit 1 ;;
      # Cold discovery: one klog retry line per attempt, then kubectl's actual
      # reason. The first line names the retry; the cause is last.
      noisy_refusal)
        i=1
        while [ "$i" -le 3 ]; do
          echo 'E0728 11:51:48.306797   82827 memcache.go:265] "Unhandled Error" err="couldn'"'"'t get current server API group list: dial tcp: lookup timed out"' >&2
          i=$((i + 1))
        done
        echo 'Error from server (BadRequest): previous terminated container "postgres" in pod "db-0" not found' >&2
        exit 1 ;;
      # Returns the log AND writes two warnings: kubectl does this for deprecation
      # notices and partial results, and both are evidence.
      warned)
        printf 'stub previous-instance line\n'
        echo 'Warning: v1 is deprecated in this build' >&2
        echo 'Warning: results are partial, 3 of 7 shards answered' >&2 ;;
      partial_reset)
        printf 'first line\nthe decisive partial line'
        echo 'error: unexpected EOF from the server' >&2
        exit 1 ;;
      # SIGKILLed part way through: 137, output kept. Our own `-k` grace produces
      # this, and so does anything else that kills the read.
      partial9) printf 'first line\nthe decisive partial line'; exit 137 ;;
      *) printf 'stub previous-instance line\n' ;;
    esac ;;
esac
exit 0
STUB
  chmod +x "$_sd/kubectl"
  # Stub `timeout` too. Without it these tests need GNU coreutils on PATH, and a
  # host lacking it does not merely fail — a missing `timeout` makes the script
  # exit 127, which reaches the SAME "could not list pods" branch the
  # unreachable-API test asserts on, so that test would pass without its stub
  # doing anything. A guard that can pass for the wrong reason is not a guard.
  cat > "$_sd/timeout" <<'STUB'
#!/bin/sh
if [ "$1" = "-k" ]; then shift 2; fi
shift
exec "$@"
STUB
  chmod +x "$_sd/timeout"
}

@test "an unreachable api leaves the reason in the output directory" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_LIST_FAIL=1 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # Without a note the reader sees an absent directory, which reads exactly like
  # "nothing crash-looped" -- the opposite of what happened.
  [ -f "$out/capture-notes.txt" ]
  grep -q 'could not list pods' "$out/capture-notes.txt"
  # kubectl's reason is the finding. "could not list pods (kubectl exit 1)" names
  # no cause, and this is the read every later line depends on -- if it fails,
  # nothing is captured at all.
  grep -q 'Forbidden' "$out/capture-notes.txt"
  # The reason is the LAST stderr line: a failing cluster-wide list is preceded by
  # one klog discovery-retry line per attempt, long enough to survive trimming.
  if grep -q 'Unhandled Error' "$out/capture-notes.txt"; then echo "FAIL: quoted the discovery noise instead of the reason"; false; fi
  rm -rf "$tmp"
}

@test "a cluster where nothing restarted says so in the output directory" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"
  rows='cozy-system|healthy-0|app|container|0'

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS="$rows" sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  [ -f "$out/capture-notes.txt" ]
  grep -q 'no container has restarted' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "the inner read budgets stay under the backstop the callers wrap this in" {
  # Three comment blocks state this arithmetic in prose -- the script header,
  # hack/cozytest.sh and hack/e2e-chainsaw/.chainsaw.yaml -- and nothing enforced
  # it, so raising a per-read timeout (or adding a `-k` grace, which counts towards
  # the same ceiling) silently moved the worst case past the backstop. Overrunning
  # it is not a slow capture: the backstop SIGKILLs the process group, so
  # capture-notes.txt ends short and the line explaining why reaches the job log
  # only, which is the failure these notes exist to prevent.
  # Read from the resolved bound, not from a literal on the call line: the list
  # read now goes through $PREVLOG_LIST_BOUND so that a host without `timeout`
  # falls back instead of exiting 127. The numbers moved to the assignment; the
  # arithmetic they feed did not.
  list="$(sed -n 's/^  PREVLOG_LIST_BOUND="timeout -k \([0-9]*\) .*"/\1/p' "$SCRIPT") $(sed -n 's/^PREVLOG_LIST_TIMEOUT=\([0-9]*\)/\1/p' "$SCRIPT")"
  read_="$(sed -n 's/^PREVLOG_READ_GRACE=\([0-9]*\)/\1/p' "$SCRIPT") $(sed -n 's/^PREVLOG_READ_TIMEOUT=\([0-9]*\)/\1/p' "$SCRIPT")"
  cap=$(sed -n 's/^MAX="${COZY_PREVLOG_MAX:-\([0-9]*\)}"/\1/p' "$SCRIPT")
  [ -n "$list" ] && [ -n "$read_" ] && [ -n "$cap" ]

  list_ceiling=$(( $(echo "$list" | cut -d' ' -f1) + $(echo "$list" | cut -d' ' -f2) ))
  read_ceiling=$(( $(echo "$read_" | cut -d' ' -f1) + $(echo "$read_" | cut -d' ' -f2) ))
  worst=$(( list_ceiling + cap * read_ceiling ))

  # The message that reports a cut-off must quote the budget that produced it,
  # otherwise it names a number no read ever used. Two bounds exist -- the list
  # read is wrapped in its own, larger one -- so what has to hold is the PAIRING:
  # every call passes the seconds belonging to the bound it also passes. Getting
  # that wrong is silent, and it puts a number in the artifact that no read used.
  pairs=$(grep -o 'prevlog_cutoff_desc "[^"]*" "\$[A-Z_]*" "\$[A-Z_]*"' "$SCRIPT" |
    sed 's/.*"\(\$[A-Z_]*\)" "\(\$[A-Z_]*\)"/\1 \2/' | LC_ALL=C sort -u)
  [ -n "$pairs" ] || { echo "FAIL: no cut-off description is derived from a bound"; false; }
  printf '%s\n' "$pairs" | while read -r secs bound; do
    case "$secs $bound" in
      '$PREVLOG_READ_TIMEOUT $PREVLOG_BOUND') ;;
      '$PREVLOG_LIST_TIMEOUT $PREVLOG_LIST_BOUND') ;;
      *) echo "FAIL: $secs is quoted for a read bounded by $bound"; exit 1 ;;
    esac
  done

  for caller in "$HACK_DIR/cozytest.sh" "$HACK_DIR/e2e-chainsaw/.chainsaw.yaml"; do
    backstop=$(sed -n 's/.*timeout -k [0-9]* \([0-9]*\) .*e2e-capture-previous-logs\.sh.*/\1/p' "$caller" | head -n 1)
    [ -n "$backstop" ]
    if [ "$worst" -gt "$backstop" ]; then
      echo "FAIL: worst case ${worst}s (list ${list_ceiling}s + ${cap} x ${read_ceiling}s) exceeds the ${backstop}s backstop in $caller"
      false
    fi
  done
}

@test "an unwritable temp dir does not become a claim about the cluster" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"
  # A path that does not exist, not one that is unwritable: root holds
  # CAP_DAC_OVERRIDE and mktemp would succeed inside a 0500 directory, so the test
  # would pass without entering the fallback. ENOENT is refused for everyone.
  PATH="$stub:$PATH" TMPDIR="$tmp/gone" \
    PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # With a predictable "$$" scratch path the redirect targets a file under a TMPDIR
  # that is not there, the read never runs, and the capture reports that no previous
  # instance was retrieved -- a statement about the cluster caused entirely by the
  # local filesystem. mktemp fails too, but a failed command substitution already
  # yields an empty string here, and `${err:-/dev/null}` then keeps the read alive.
  if grep -q 'no previous instance retrieved' "$out/capture-notes.txt" 2>/dev/null; then
    echo "FAIL: blamed the cluster for an unwritable TMPDIR"
    cat "$out/capture-notes.txt"
    false
  fi
  [ "$(ls "$out" | grep -c '\.log$')" -eq 1 ]
  rm -rf "$tmp"
}

@test "hitting the container cap records the overflow next to the captured logs" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"
  rows="$(printf '%s\n' \
    'tenant-test|db-0|postgres|container|4' \
    'tenant-test|db-1|postgres|container|3' \
    'cozy-system|other-0|app|container|2')"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS="$rows" COZY_PREVLOG_MAX=1 \
    sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  [ -f "$out/capture-notes.txt" ]
  grep -q 'COZY_PREVLOG_MAX=1 cap' "$out/capture-notes.txt"
  grep -q '2 more restarted container(s) NOT captured' "$out/capture-notes.txt"
  # The cap still applied: one dump, not three.
  [ "$(ls "$out" | grep -c '\.log$')" -eq 1 ]
  rm -rf "$tmp"
}

@test "a missing timeout is named as a local dependency, not as a cluster failure" {
  tmp="$(mktemp -d)"
  bin="$tmp/bin"; mkdir -p "$bin"
  out="$tmp/previous-logs"
  # A PATH with everything the script needs EXCEPT timeout. Not a blanket empty
  # PATH: that would also remove the interpreter and the script would die before
  # its own fallback, passing for the wrong reason.
  for b in sh mktemp grep sed awk cat rm mkdir tail head cut ls date printf; do
    p=$(command -v "$b" 2>/dev/null) && ln -sf "$p" "$bin/$b"
  done
  cat > "$bin/kubectl" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod +x "$bin/kubectl"

  PATH="$bin" sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # Without the fallback every `timeout` call exits 127, and the script reports
  # "could not list pods (kubectl exit 127): timeout: command not found" --
  # attributing to kubectl a failure in which kubectl never ran, inside the
  # artifact rather than only the job log.
  [ -f "$out/capture-notes.txt" ]
  if grep -q 'kubectl exit 127' "$out/capture-notes.txt"; then
    echo "FAIL: a missing local binary is reported as a kubectl failure"
    cat "$out/capture-notes.txt"
    false
  fi
  grep -q 'timeout is not on PATH' "$out/capture-notes.txt"
  grep -q 'not a statement about the cluster' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "a missing kubectl leaves a note rather than an empty directory" {
  tmp="$(mktemp -d)"
  bin="$tmp/bin"; mkdir -p "$bin"
  out="$tmp/previous-logs"
  # A PATH carrying only what the script needs before the kubectl check, so the
  # check is the only thing that fails. Blanking PATH outright would also take
  # away the interpreter and mkdir, and the script would die before its own
  # guard -- passing the test for the wrong reason.
  ln -s "$(command -v mkdir)" "$bin/mkdir"

  # One caller pre-gates on kubectl, the other does not. A silent exit here
  # leaves the artifact carrying an empty directory, which reads as "looked,
  # found no crash-loops".
  PATH="$bin" /bin/sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  [ -f "$out/capture-notes.txt" ]
  grep -q 'kubectl is not on PATH' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "each captured log says which tail bound it was written under" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # `--tail` drops the OLDEST lines and nothing in the output says so, so a file
  # beginning mid-stream is indistinguishable from a whole one. This is the case
  # where that matters most: the previous instance is already gone from the
  # cluster, so this file is the only copy, and the bound here is ten times
  # tighter than the per-pod one. Naming it in the job log is not enough -- the
  # job log expires and is not in the tarball at all.
  # Beside the log, not inside it: a log read in full is a complete artifact and
  # these controllers log JSON per line, so a trailing prose line breaks every
  # parser that could read the file before.
  if grep -q 'holds at most the last' "$file"; then
    echo "FAIL: the bound note is inside a successfully captured log"
    false
  fi
  if ! grep -q 'holds at most the last 200 lines' "$out/capture-notes.txt"; then
    echo "FAIL: the captured log does not name the tail bound it was written under"
    cat "$file"
    false
  fi
  # The bound named is the one that was applied, not the default echoed back.
  rm -rf "$out"
  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    COZY_PREVLOG_TAIL=7 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1
  grep -q 'last 7 lines' "$out/capture-notes.txt"
  grep -q 'COZY_PREVLOG_TAIL=7' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "a warning alongside a captured log is kept whole, not dropped" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=warned sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # The log itself stays clean: it was returned in full, and these controllers
  # log JSON per line. But "not in the log" and "not in the artifact" are
  # different outcomes, and only the first is intended.
  if grep -q 'deprecated' "$file"; then
    echo "FAIL: the warning is inside the log"; false
  fi
  # Both lines, not just the last: with no klog preamble to skip, each is a
  # warning of its own and picking one drops the other.
  grep -q 'v1 is deprecated' "$out/capture-notes.txt" || {
    echo "FAIL: the first warning was dropped"; cat "$out/capture-notes.txt"; false
  }
  grep -q 'results are partial' "$out/capture-notes.txt" || {
    echo "FAIL: the second warning was dropped"; cat "$out/capture-notes.txt"; false
  }
  rm -rf "$tmp"
}

@test "a clean read that logged nothing keeps what kubectl said about it" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=warned_empty sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # kubectl exited 0, returned no log, and still spoke. Gating the warning copy on
  # the log being non-empty drops the message entirely, and the next line then
  # reports "produced no output" -- a positive statement about the container,
  # manufactured while kubectl was in the middle of contradicting it.
  grep -q 'results are partial' "$out/capture-notes.txt" || {
    echo "FAIL: kubectl spoke on an empty read and the message was dropped"
    cat "$out/capture-notes.txt"
    false
  }
  # And the two lines must agree: no bare claim of silence beside a quoted message.
  if grep -q 'produced no output' "$out/capture-notes.txt"; then
    echo "FAIL: reported silence in the same file that quotes what kubectl said"
    cat "$out/capture-notes.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "a partial capture still records the tail bound it was read under" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=partial sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # The cut took the newest lines, `--tail` took the oldest, and only the cut was
  # recorded. A reader then treats the first line of the file as the first line
  # the container wrote.
  grep -q 'holds at most the last' "$out/capture-notes.txt" || {
    echo "FAIL: a partial capture does not record its tail bound"
    cat "$out/capture-notes.txt"
    false
  }
  rm -rf "$tmp"
}

@test "the tail bound is stated once for the run, not once per container" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4
tenant-test|db-0|sidecar|container|2
cozy-system|api-0|app|container|1' \
    sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # The bound is identical for every log the capture writes, so repeating it per
  # container put up to COZY_PREVLOG_MAX copies of one sentence into the file a
  # reader holding only the artifact opens FIRST -- the same file that has to make
  # a short capture's reasons findable. Three containers, one statement.
  n=$(grep -c 'holds at most the last' "$out/capture-notes.txt" || true)
  if [ "$n" -ne 1 ]; then
    echo "FAIL: expected exactly one tail-bound note for the run, found $n"
    cat "$out/capture-notes.txt"
    false
  fi
  # Still says which bound, and still applies to the whole directory rather than
  # to one named file, or the reader cannot tell which logs it covers.
  grep -q 'every previous-instance log in this directory holds at most the last 200 lines' \
    "$out/capture-notes.txt"
  # Three captures really did happen, so the single note is not an artefact of the
  # walk having stopped after one.
  [ "$(grep -c . "$out/capture-notes.txt")" -gt 1 ]
  rm -rf "$tmp"
}

@test "the tail note starts a line of its own after a killed stream" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=partial sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # A killed read drops the NEWEST lines; --tail drops the OLDEST. They are not
  # interchangeable, and the tail note alone is worse here than no note at all: it
  # gives the reader a confident explanation of why the beginning is missing while
  # saying nothing about the missing end, so a log cut off mid-stream reads as a
  # complete-to-the-last-line window. The TRUNCATED line reaches capture-notes.txt,
  # but a reader standing in the .log is not looking there.
  if ! grep -q '^\[capture-previous-logs\] TRUNCATED: this log ends here' "$file"; then
    echo "FAIL: a killed read is not marked as cut off inside the log itself"
    cat "$file"
    false
  fi
  # And it must NOT claim the file is a clean tail window, which is the false half.
  if grep -q 'holds at most the last' "$file"; then
    echo "FAIL: a truncated capture claims to be a complete tail window"
    cat "$file"
    false
  fi
  # The marker starts a line of its own: a read killed mid-stream leaves a partial
  # final line, so appending blind would glue the marker onto it, corrupting the
  # decisive last line and making a start-of-line grep miss it.
  grep -q '^the decisive partial line$' "$file"
  rm -rf "$tmp"
}

@test "a partial capture that was not a timeout does not claim to be one" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=partial_reset sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # kubectl can write part of the stream and then fail on its own terms, with no
  # clock having run out. Naming the read timeout there states a cause that was
  # never observed -- the same defect as a tail note on a truncated file, in the
  # other direction: a confident explanation that happens to be false.
  if grep -qE 'its own [0-9]+s timeout|SIGKILL' "$file"; then
    echo "FAIL: a non-timeout failure is reported as the read timeout"
    cat "$file"
    false
  fi
  grep -q '^\[capture-previous-logs\] TRUNCATED: this log ends here because kubectl exited 1' "$file"
  # kubectl's own reason survives into the file rather than only the exit code.
  grep -q 'unexpected EOF' "$file"
  rm -rf "$tmp"
}

@test "a capture killed by a signal does not assert whose signal it was" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=partial9 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # 137 is 128+SIGKILL. Our own `-k` grace produces it, and so does the OOM killer
  # on a loaded runner or a teardown signalling the process group -- the exit
  # status does not say which. The cut-off branch is right either way, since the
  # capture ends before the log did; "timed out after 18s" is not, and a read
  # killed at second two then reads as one that waited the full eighteen.
  grep -q '^\[capture-previous-logs\] TRUNCATED: this log ends here' "$file"
  grep -q 'SIGKILL' "$file"
  grep -q 'does not tell apart' "$file"
  rm -rf "$tmp"
}

@test "a capture that ran out its own clock says so without a disjunction" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=partial sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # The converse, and why the hedge is written per status rather than over both:
  # 124 comes from `timeout` and from nothing else, so hedging it too would trade
  # one false note for a vague one everywhere.
  grep -q 'its own 18s timeout' "$file"
  if grep -q 'SIGKILL' "$file"; then
    echo "FAIL: hedged a 124, which only timeout produces"
    cat "$file"
    false
  fi
  # capture-notes.txt describes the SAME read and has to agree with the marker in
  # the log. Asserting only on the .log left the notes free to call a 124 "kubectl
  # exit 124" -- a status this script's own clock produced, attributed to kubectl,
  # in the file a reader opens first.
  grep -q 'cut off by' "$out/capture-notes.txt" || {
    echo "FAIL: the notes describe a timeout as a plain kubectl exit"
    cat "$out/capture-notes.txt"
    false
  }
  if grep -q 'TRUNCATED (kubectl exit' "$out/capture-notes.txt"; then
    echo "FAIL: the notes and the log disagree about the same read"
    cat "$out/capture-notes.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an unusable output directory is not reported as kubectl failing" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  # A FILE where the parent directory should be, so `mkdir -p` fails for every
  # uid. A chmod-based fixture would pass for root, which is how CI runs this.
  : > "$tmp/blocked"
  out="$tmp/blocked/previous-logs"

  run_out="$(PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    sh "$SCRIPT" "$out" tenant-test 2>&1 || true)"

  # A redirection that cannot open its target fails before kubectl runs, and the
  # shell status lands in the branch that names kubectl. Once per container, the
  # report then blames the cluster for a directory the caller got wrong.
  if printf '%s\n' "$run_out" | grep -q 'kubectl exit'; then
    echo "FAIL: a local filesystem condition is reported as kubectl failing"
    printf '%s\n' "$run_out"
    false
  fi
  printf '%s\n' "$run_out" | grep -q 'cannot write to'
  printf '%s\n' "$run_out" | grep -q 'says nothing about whether anything crash-looped'
  rm -rf "$tmp"
}

@test "a zero tail is refused rather than turned into a claim about the cluster" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    COZY_PREVLOG_TAIL=0 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # `--tail=0` returns nothing and kubectl still exits 0, so the empty-output
  # branch would delete the capture and report that the previous instance
  # "produced no output" -- a positive statement about the cluster produced
  # entirely by a typo, which also discards the only remaining copy of a log the
  # kubelet has already collected.
  if grep -q 'produced no output' "$out/capture-notes.txt"; then
    echo "FAIL: a zero tail was reported as the container having logged nothing"
    cat "$out/capture-notes.txt"
    false
  fi
  grep -q "ignoring COZY_PREVLOG_TAIL='0'" "$out/capture-notes.txt"
  # The fallback applied, so the capture is there and names the bound that was used.
  grep -q 'last 200 lines' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "a tail of -1 claims no bound, because none was applied" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    COZY_PREVLOG_TAIL=-1 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  file="$(ls "$out"/*.log)"
  # -1 is the documented spelling for "the whole log". A note reading "holds at
  # most the last -1 lines" would describe a bound that was never applied, which
  # is the same class as claiming a truncated file is complete: a statement about
  # the artifact that the artifact does not support.
  if grep -q 'holds at most' "$file"; then
    echo "FAIL: an unbounded read claims a tail bound"
    cat "$file"
    false
  fi
  # And -1 is not treated as malformed: it is a supported request, not a typo.
  if grep -q 'ignoring COZY_PREVLOG_TAIL' "$out/capture-notes.txt"; then
    echo "FAIL: -1 was rejected as malformed"
    false
  fi
  rm -rf "$tmp"
}

@test "an oversized knob falls back instead of silently losing every row" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"
  huge=999999999999999999999999999999

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    COZY_PREVLOG_MAX="$huge" sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # A digits-only value that `test` and `head` cannot compute with does not merely
  # fail to cap -- under dash `[ -eq ]` errors and `head` rejects the count, so
  # prevlog_cap emits nothing and the caller reports zero containers kept "because
  # the cap was reached". The cap that broke collection would be named as the
  # reason collection found nothing.
  grep -q "ignoring COZY_PREVLOG_MAX='$huge'" "$out/capture-notes.txt"
  # The fallback applied, so the row survived rather than being lost to the knob.
  [ "$(ls "$out" | grep -c '\.log$')" -eq 1 ]
  if grep -q 'capturing previous-instance logs for 0 of' "$out/capture-notes.txt"; then
    echo "FAIL: an unusable cap was reported as having capped everything away"
    cat "$out/capture-notes.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "an uncapturable reason is not reported as no message from kubectl" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  # mktemp fails, so the stderr scratch file never exists and $reason stays empty.
  # `${reason:-no message from kubectl}` then states a fact about kubectl that was
  # never observed.
  PATH="$stub:$PATH" TMPDIR=/nonexistent-prevlog-probe \
    PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=norefusal sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  [ -f "$out/capture-notes.txt" ] || { echo "FAIL: no notes written at all"; false; }
  if grep -q 'no message from kubectl' "$out/capture-notes.txt"; then
    echo "FAIL: an uncapturable message is reported as kubectl having sent none"
    cat "$out/capture-notes.txt"
    false
  fi
  rm -rf "$tmp"
}

@test "the reason quoted is kubectl's, not the klog retry line above it" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS='tenant-test|db-0|postgres|container|4' \
    PREVLOG_STUB_LOG_MODE=noisy_refusal sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # A read against cold discovery is preceded by one klog retry line per attempt,
  # and kubectl's actionable message comes last. Taking the first line names the
  # retry instead of the cause -- and since this value now reaches the artifact
  # rather than only the job log, the artifact would ship the wrong cause. Every
  # other stderr quote in these scripts already takes the last line; this one did
  # not, and nothing pinned which end it used.
  if grep -q "couldn't get current server API group list" "$out/capture-notes.txt"; then
    echo "FAIL: quoted the klog retry line instead of kubectl's reason"
    cat "$out/capture-notes.txt"
    false
  fi
  grep -q 'previous terminated container' "$out/capture-notes.txt"
  rm -rf "$tmp"
}

@test "a second capture run into the same directory is separated from the first" {
  tmp="$(mktemp -d)"
  stub="$tmp/bin"; mkdir -p "$stub"; prevlog_stub_dir "$stub"
  out="$tmp/previous-logs"
  rows='cozy-system|healthy-0|app|container|0'

  PATH="$stub:$PATH" PREVLOG_STUB_ROWS="$rows" sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1
  PATH="$stub:$PATH" PREVLOG_STUB_LIST_FAIL=1 sh "$SCRIPT" "$out" tenant-test >/dev/null 2>&1

  # Notes are appended, so without a separator one run's "nothing restarted"
  # sits flush against the next run's "could not list pods" and the file stops
  # being able to explain either.
  grep -q -- '--- new capture run ---' "$out/capture-notes.txt"
  grep -q 'no container has restarted' "$out/capture-notes.txt"
  grep -q 'could not list pods' "$out/capture-notes.txt"
  rm -rf "$tmp"
}
