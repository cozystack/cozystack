#!/usr/bin/env bats
# Regression coverage for the sandbox node CPU time capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why nothing beside it answers the same thing:
# the worker captures describe a cgroup, so they can say what the tenant worker
# was allowed and what it consumed, and they are blind to the layer under the
# sandbox. A guest that is runnable and never scheduled looks from the cgroup
# like a guest that asked for nothing. Whether the sandbox node itself was given
# its turn is recorded in one place only -- the steal column of its own
# /proc/stat -- and the kubelet endpoints this tree already reads do not carry
# it. So the failure this suite guards against is not a wrong number; it is an
# empty directory, which reads as a sandbox that lost no time while nothing was
# ever asked.

kubectl_calls=/dev/null
kubectl_rows='srv1|192.0.2.11'
kubectl_list_rc=0
kubectl_list_stderr=
talosctl_calls=/dev/null
talosctl_rc=0
talosctl_stderr=
talosctl_output='cpu  10 0 5 900 1 0 0 7 4000 0
cpu0 10 0 5 900 1 0 0 7 4000 0'
timeout_calls=/dev/null
timeout_fail_node=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"
  [ -z "${kubectl_list_stderr}" ] || printf '%s\n' "${kubectl_list_stderr}" >&2
  [ "${kubectl_list_rc}" -eq 0 ] || return "${kubectl_list_rc}"
  # Quoted, or the mock decides the shape of the answer before the code under
  # test sees it: jsonpath puts a dual-stack node's two addresses in one field,
  # separated by a space, and an unquoted expansion splits that into two rows
  # here -- which is exactly the input the walk has a branch for, and the branch
  # would then be unreachable from this suite.
  [ -z "${kubectl_rows}" ] || printf '%s\n' "${kubectl_rows}"
  return 0
}

talosctl() {
  printf '%s\n' "$*" >>"${talosctl_calls}"
  [ -z "${talosctl_stderr}" ] || printf '%s\n' "${talosctl_stderr}" >&2
  # Output before the status, because a read cut off part way has already
  # written what it managed to read. A mock that returned first could only ever
  # produce empty-plus-failure, leaving the arm that labels a partial capture
  # unreachable in tests while being the likeliest one in production.
  [ -z "${talosctl_output}" ] || printf '%s\n' "${talosctl_output}"
  return "${talosctl_rc}"
}

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # Return 97 rather than running the command when the wrapper is not the
  # bounded form: a read that lost its ceiling must not pass as a read.
  [ "${1:-}" = -k ] || return 97
  shift 3
  "$@" || command_rc=$?
  if [ -n "${timeout_fail_node}" ]; then
    case " $* " in
      *"${timeout_fail_node}"*) return 124 ;;
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
  ( set +x; cozy_capture_sandbox_node_cpu_time "${1:-1}" ) || _rc=$?
  return "${_rc}"
}

stage() {
  COZY_REPORT_DIR="$1/report"
  COZY_SNAPSHOT_NAME=steal-smoke
  printf 'context: e2e\n' >"$1/talosconfig"
  export TALOSCONFIG="$1/talosconfig"
}

@test "the node's own CPU accounting is read over the Talos API" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture

  # The endpoint and the node are both the address, because the e2e talosconfig
  # carries no endpoints: one without the other reaches nothing at all.
  assert_file_contains '-e 192.0.2.11 -n 192.0.2.11 read /proc/stat' "$talosctl_calls"
  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'cpu  10 0 5 900 1 0 0 7 4000 0' "$capture"
  assert_file_lacks_pattern 'unknown' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  rm -rf "$tmp"
}

@test "the column legend travels with the numbers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture

  # /proc/stat carries no header, and the two columns that matter here sit next
  # to each other: steal is the eighth number after the label and guest is the
  # ninth. Guest is large on a node running VMs and is already counted inside
  # user, so reading the ninth as the eighth turns a fraction of a percent into
  # twenty-odd -- which has already happened once against these nodes. The
  # legend is what stops the next reader repeating it, and it is only useful
  # where the numbers are.
  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'The eighth number is steal and the ninth is guest' "$capture"
  # The pairing instruction rides with the legend, on the one arm that holds a
  # whole reading, and so does the claim that counters were sampled inside the
  # brackets -- the stamp itself only says when the read was attempted.
  assert_file_contains 'subtracting this node file from its sibling' "$capture"
  assert_file_contains 'counters were sampled somewhere inside each read' "$capture"
  rm -rf "$tmp"
}

@test "each reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture

  # Here the stamp is the only timing there is. A cAdvisor row carries its own
  # sample time; a /proc/stat row carries none, so without this the gap between
  # two readings of a node is unrecoverable from the artifact and a steal rate
  # would have to be computed from a number the block does not promise.
  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'read attempted from' "$capture"
  # Both ends, for the reason the sibling suite states: one stamp hides the read
  # duration inside the interval, and that duration is the variable this
  # collector is deployed against.
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "each read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture

  # An unbounded read here would hold the whole diagnostics block against a node
  # whose Talos API is up but wedged, and the phase budget bounds when a read
  # may START, never how long one already running may take. Named by their verbs
  # rather than by the bound, because both carry the same one and a bare check
  # for it would be satisfied twice by whichever read survived.
  assert_file_contains '-k 5 20 kubectl get nodes' "$timeout_calls"
  assert_file_contains '-k 5 20 talosctl' "$timeout_calls"
  assert_file_contains '--request-timeout=20s' "$kubectl_calls"
  assert_file_contains '[capture exit code: 0]' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  rm -rf "$tmp"
}

@test "a lowered read budget moves both of this collector's bounds" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Set after sourcing, which is how a caller and a test both set it. A value
  # read only at assignment time would reach `timeout` unchecked here.
  COZY_DIAG_READ_TIMEOUT=4

  run_capture

  assert_file_contains '-k 5 4 kubectl get nodes' "$timeout_calls"
  assert_file_contains '-k 5 4 talosctl' "$timeout_calls"
  assert_file_contains '--request-timeout=4s' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a zero read budget is corrected rather than disabling the bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # `timeout -k 5 0` disables the timeout outright, so a zero assigned after
  # sourcing would restore the unbounded read this bound exists to remove.
  COZY_DIAG_READ_TIMEOUT=0

  run_capture

  assert_file_lacks_pattern '^-k 5 0 ' "$timeout_calls"
  assert_file_contains "-k 5 $COZY_DIAG_READ_TIMEOUT_DEFAULT talosctl" "$timeout_calls"
  rm -rf "$tmp"
}

@test "no talosctl on PATH is recorded rather than left as an empty directory" {
  # A subprocess with a stripped PATH, because this file mocks `talosctl` as a
  # shell function and `command -v` finds functions -- the arm cannot be reached
  # from inside a test body. Same device the sibling suites use, and the PATH is
  # staged rather than emptied: the collector shells out before it gets to the
  # check, so an empty PATH would fail this for the wrong reason and read as the
  # arm being broken.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in mkdir grep mv rm; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
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
  if [ -e "$tmp/bin/talosctl" ]; then
    echo "FAIL: talosctl leaked into the stripped PATH; this test would prove nothing" >&2
    return 1
  fi
  printf 'context: e2e\n' >"$tmp/talosconfig"
  rc=0

  # Assigned inside the subprocess rather than in front of it: bash resolves the
  # command name with the PATH the assignment sets, so `PATH=... bash` cannot
  # find bash itself.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=steal-smoke
    TALOSCONFIG='"$tmp"'/talosconfig
    PATH='"$tmp"'/bin
    cozy_capture_sandbox_node_cpu_time 1
  ' 2>&1) || rc=$?
  printf '%s\n' "$out" >"$tmp/out"

  [ "$rc" -ne 0 ]
  marker="$tmp/report/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-FAILED.txt"
  assert_file_contains 'talosctl is not on PATH' "$marker"
  # The distinction the marker exists for, spelled out in the artifact: a tool
  # that was never run says nothing about the nodes, and the reading a missing
  # file invites is that they lost no time.
  assert_file_contains 'not a reading that they lost no time' "$marker"
  rm -rf "$tmp"
}

@test "no talosconfig is recorded as a config that was missing, not as a node that refused" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  export TALOSCONFIG="$tmp/absent"
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  assert_file_contains 'no sandbox talosconfig at' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-FAILED.txt"
  # And nothing was asked, so no per-node file may claim otherwise.
  [ ! -f "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt" ]
  rm -rf "$tmp"
}

@test "a node listing that failed is not reported as a cluster with no nodes" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  kubectl_list_rc=1
  kubectl_list_stderr='error: You must be logged in to the server (Unauthorized)'
  rc=0

  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  marker="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-FAILED.txt"
  assert_file_contains 'failed to list sandbox nodes' "$marker"
  # The status alone names none of the ways a listing fails -- Unauthorized, a
  # refused connection and an unrecognised kind are all exit 1 -- so the stderr
  # that says which is kept.
  assert_file_contains 'Unauthorized' "$marker"
  rm -rf "$tmp"
}

@test "a warning on a successful listing is kept apart from a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # kubectl writes deprecation and partial-result warnings on stderr with a zero
  # exit, so which file stderr lands in is decided by the status and not by
  # something having been written.
  kubectl_list_stderr='Warning: v1 Node is deprecated'
  dir="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1"

  run_capture

  assert_file_contains 'Warning: v1 Node is deprecated' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  rm -rf "$tmp"
}

@test "a node with no InternalIP says why it was not asked" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  kubectl_rows='srv1|'

  run_capture

  # An absent file would be the same artifact as a node that was never listed.
  # The Talos API is reached on that address, so its absence is the reason this
  # node carries no numbers, and the reason is what the file holds.
  assert_file_contains 'reports no InternalIP' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  rm -rf "$tmp"
}

@test "a dual-stack node is one node, dialled on its first address" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # jsonpath emits every matching address into the same field, space separated,
  # so a node with an IPv4 and an IPv6 InternalIP arrives as one row carrying
  # two addresses. Both halves of that are wrong if taken literally: the pair
  # concatenated is not an address to dial, and the second one is not a node.
  kubectl_rows='srv1|192.0.2.11 2001:db8::11
srv2|192.0.2.12 2001:db8::12
srv3|192.0.2.13 2001:db8::13
srv4|192.0.2.14 2001:db8::14'

  run_capture

  assert_file_contains '-e 192.0.2.11 -n 192.0.2.11 read /proc/stat' "$talosctl_calls"
  assert_file_lacks_pattern '2001:db8' "$talosctl_calls"
  # And the count in the truncation marker is nodes, not addresses. The marker
  # exists to say a short listing was truncated rather than small, so a total
  # that counts each node twice is the one number in it that must not be wrong.
  assert_file_contains 'capture stopped after 3 nodes; 4 were listed in total' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-TRUNCATED.txt"
  rm -rf "$tmp"
}

@test "a node whose read timed out does not stop the next node" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  kubectl_rows='srv1|192.0.2.11
srv2|192.0.2.12'
  timeout_fail_node=192.0.2.11

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1"
  assert_file_contains '[capture exit code: 124]' "$dir/srv1.txt"
  assert_file_contains 'cpu  10 0 5 900 1 0 0 7 4000 0' "$dir/srv2.txt"
  rm -rf "$tmp"
}

@test "a read that never answered is recorded as unread, not as a node that lost nothing" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  talosctl_rc=1
  talosctl_output=
  talosctl_stderr='rpc error: code = Unavailable desc = connection error'

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'is unknown: the Talos API was not read' "$capture"
  # This file holds no counters at all, so a reader following a pairing
  # instruction here would subtract nothing from a healthy sibling and read that
  # sibling's whole cumulative value as the delta -- steal inflated to the node's
  # entire uptime, manufacturing the finding this collector looks for.
  assert_file_lacks_pattern 'subtracting this node file from its sibling' "$capture"
  # Nor may the stamp assert that anything was sampled between its two instants.
  # When the read was attempted is true on every arm; that counters exist inside
  # that bracket is exactly what this arm has just denied, and two consecutive
  # lines contradicting each other is the reading failure this tree is written
  # against.
  assert_file_lacks_pattern 'counters were sampled' "$capture"
  assert_file_contains 'read attempted from' "$capture"
  # The note has to name a file that exists. There is no bare read-error.log in
  # the directory -- each is named for its node, and the walk writes one per
  # node -- so a note pointing at the bare name sends the reader after an
  # artifact that was never written, which is the same harm as silence reported
  # as an answer, one indirection out.
  assert_file_contains 'beside this file, named for the same node' "$capture"
  assert_file_contains 'connection error' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.read-error.log"
  rm -rf "$tmp"
}

@test "a read killed without a word does not point at a file that was never written" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # `timeout` SIGKILLs its child, so the dominant failure arrives non-zero with
  # nothing on either stream. A note naming an error log here sends the reader
  # after evidence that does not exist.
  talosctl_rc=1
  talosctl_output=
  talosctl_stderr=

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'died without a word on either stream' "$capture"
  [ ! -f "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.read-error.log" ]
  rm -rf "$tmp"
}

@test "a read cut off part way is marked incomplete rather than presented whole" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  # Output arrived and the read still failed, which is what a stream cut off
  # part way through looks like. Read as complete, its numbers are a smaller
  # total over the same interval -- a steal figure biased toward zero, in the
  # direction that ends the investigation.
  talosctl_rc=124

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'these counters are incomplete' "$capture"
  # And the legend, which is a statement about a complete row, must not be
  # attached to one that was truncated.
  assert_file_lacks_pattern 'eighth number is steal' "$capture"
  # Nor the instruction to pair it. A difference taken against a prefix
  # understates the counter by whatever the read did not return, which for steal
  # is the direction that says the node was given every turn it asked for.
  assert_file_lacks_pattern 'subtracting this node file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "a read that succeeded and returned nothing is not filed as a reading" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  talosctl_output=

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv1.txt"
  assert_file_contains 'the read succeeded and returned nothing' "$capture"
  rm -rf "$tmp"
}

@test "the walk is capped and names both counts when it stops" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"
  kubectl_rows='srv1|192.0.2.11
srv2|192.0.2.12
srv3|192.0.2.13
srv4|192.0.2.14'

  run_capture

  # A short listing otherwise reads as a small cluster rather than a truncated
  # walk, so a cap that fired is recorded with both counts.
  assert_file_contains 'capture stopped after 3 nodes; 4 were listed in total' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-TRUNCATED.txt"
  [ ! -f "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/srv4.txt" ]
  rm -rf "$tmp"
}

@test "a cluster within the cap leaves no truncation marker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1/COLLECTION-TRUNCATED.txt" ]
  rm -rf "$tmp"
}

@test "the sample number decides which reading a file belongs to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  stage "$tmp"

  run_capture 2

  # The pair is the instrument and the counters are cumulative, so a second
  # reading written over the first leaves one file and no interval -- collected,
  # and unable to answer the question it was collected for.
  assert_file_contains 'cpu  10 0 5 900 1 0 0 7 4000 0' \
    "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-2/srv1.txt"
  [ ! -d "$COZY_REPORT_DIR/snapshots/steal-smoke/sandbox-host-cpu-time/sample-1" ]
  rm -rf "$tmp"
}
