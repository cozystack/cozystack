#!/usr/bin/env bats
# Regression coverage for the tenant-worker CPU throttling capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why nothing else in the tree answers it: when
# a worker misses the node-Ready deadline, the sandbox node it runs on is
# routinely half idle while the guest makes no progress. Node-level utilisation
# and the node's dmesg both look healthy in that state, so "the host was
# starved" and "the VM was held at its own CFS ceiling" produce the same
# evidence from outside. Only the container's own throttling counters separate
# them, and a capture that reports an unread kubelet as a quiet one would hand
# the next reader the same ambiguity while looking like it had resolved it.
# These tests pin the separation, not the reading.

kubectl_calls=/dev/null
kubectl_node_names=
kubectl_list_rc=0
kubectl_list_stderr=
kubectl_raw_rc=0
kubectl_raw_stderr=
# Two of the fixtures in this file claim to be wire shapes, and the rest do not.
# This one and the uncapped test's single line are the two cAdvisor can actually
# produce -- every family for a capped container, the period alone for one
# with no limit, the two sides of the same Quota != 0 gate. Usage sits outside
# that gate and is published for any container at all, so it belongs in both
# shapes; the uncapped fixture stages only the line its own assertion needs, as
# the rest of this file does. Every other fixture
# below stages only the lines its own assertion needs, so a missing sibling
# series there means "not relevant here" rather than "absent on the wire".
kubectl_raw_output='container_cpu_usage_seconds_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 118.4
container_cpu_cfs_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 51200
container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 4211
container_cpu_cfs_throttled_seconds_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 12.5
container_spec_cpu_quota{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 100000
container_spec_cpu_period{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 100000'
timeout_calls=/dev/null
timeout_fail_node=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"

  if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then
    [ -z "${kubectl_list_stderr}" ] || printf '%s\n' "${kubectl_list_stderr}" >&2
    [ "${kubectl_list_rc}" -eq 0 ] || return "${kubectl_list_rc}"
    [ -z "${kubectl_node_names}" ] || printf '%s\n' ${kubectl_node_names}
    return 0
  fi

  if [ "${1:-}" = get ] && [ "${2:-}" = --raw ]; then
    [ -z "${kubectl_raw_stderr}" ] || printf '%s\n' "${kubectl_raw_stderr}" >&2
    # Output is emitted before the status is honoured, because a read that is
    # cut off part way through has already written what it managed to read. A
    # stub that returns first can only ever produce empty-plus-failure, and
    # the branch that labels a partial capture would be unreachable in tests
    # while being the likeliest one in production.
    [ -z "${kubectl_raw_output}" ] || printf '%s\n' "${kubectl_raw_output}"
    return "${kubectl_raw_rc}"
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
  # exists and does not carry the label". awk exits 2 on an unreadable path,
  # which folded into an if reads the same way, so the file is checked first.
  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Branched on awk's exact status, not on truthiness. awk exits 1 for "no line
  # matched" and 2 for "I could not evaluate this" -- an unparseable regex, most
  # of all. Folded into `if ... then fail; fi`, both land in the passing branch,
  # so the assertion "this pattern is absent" is satisfied by the matcher giving
  # up. That is the wrong direction for a negative assertion: a positive one
  # fails loudly when its matcher breaks and gets fixed, this one goes green and
  # stays green. Every claim in this file about something NOT appearing rests on
  # the three-way split below.
  # `|| _rc=$?` rather than a bare call: both runners set -e, and awk exiting 1
  # for "no match" -- the ordinary success case here -- would otherwise abort the
  # test before the branch below ever runs. The `if` form this replaces was
  # exempt only because conditions are exempt.
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

# COZY_REPORT_DIR is set inline in each test below rather than through a helper,
# which is the opposite of what the node-join suite does. The difference is
# deliberate and shrinks to one line: there the assignment carries a ten-line
# explanation that would otherwise stand in twenty copies, so the helper exists
# to hold the explanation, not the assignment. Here it is a bare one-liner with
# nothing to deduplicate, and a helper would buy indirection at no saving. The
# reason it must be set at all is the same in both files and lives there.
#
# The function under test redirects a command's stderr into a report file and
# decides which artifact to write from it. cozytest.sh runs under `set -x`, so
# the tracer's own line for that command lands on the very stderr being
# redirected, and every assertion about those files would be reading the
# tracer. Call through this wrapper so the sinks hold what production writes.
run_capture() {
  local _rc=0
  ( set +x; cozy_capture_tenant_worker_cpu_throttle 1 ) || _rc=$?
  return "${_rc}"
}

@test "the phase-start warning names this collector as the exception it is" {
  # That warning is what an operator reads in the CI log when `timeout` is
  # missing, and the sibling suite treats its collector list as load-bearing
  # rather than decorative -- it says which collectors do still fail that way.
  # This one calls `timeout` directly, which puts it in the named class, and
  # then does NOT fail that way because it carries a fallback. Unnamed, the
  # sentence sends the reader to the opposite conclusion about the one collector
  # whose behaviour differs, and a comment in the source does not reach them.
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Anchored on the phrase unique to the phase warning itself: the bracket-pair
  # captures write their own [bounds] notes carrying "timeout is not on PATH",
  # so the first match of that phrase is no longer this sentence.
  warn=$(grep -n 'run UNBOUNDED' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$warn" ]; then
    echo "expected the phase to still warn when timeout is missing" >&2
    return 1
  fi
  line=$(sed -n "${warn}p" "$lib")
  case "$line" in
    *'exit 127 and collect nothing'*) ;;
    *) echo "expected the warning to still name the 127 class it is contrasting against" >&2; return 1 ;;
  esac
  # The sentence went through two false shapes before this one: "except the
  # worker CPU throttling capture" (reads as sole exception, and ghcr-mirror has
  # the same shape) and then "the two that carry a fallback" (there are three --
  # talos-image-cache guards its call too). Both were enumerations, and an
  # enumeration of a growing set is wrong the next time someone adds to it.
  # What is pinned now is the DISCRIMINATOR: guarding the call with command -v
  # is what separates the two outcomes, and every collector that guards is
  # named. A fourth one added without a mention fails this.
  case "$line" in
    *'guard the call with command -v'*) ;;
    *) echo "expected the warning to state what separates the two outcomes" >&2; return 1 ;;
  esac
  for c in 'CPU throttling' 'ghcr-mirror' 'talos-image-cache'; do
    case "$line" in
      *"$c"*) ;;
      *) echo "expected the warning to name the guarded collector: $c" >&2; return 1 ;;
    esac
  done
}

@test "a negative assertion fails when its own matcher cannot evaluate" {
  # The helper below every "must not appear" claim in this file. Folded into an
  # `if`, awk exiting 2 for an unparseable regex lands in the same branch as
  # exiting 1 for "no match", so the assertion passes without having looked.
  # What rides on it here are the claims that the artifact does NOT say
  # something -- that a wedged kubelet was not written up as one that answered,
  # that a capture was not labelled uncapped, that a log which was never
  # written is not named. Those are the collector's own contract stated
  # negatively, so a matcher that fails open turns each of them into a sentence
  # nobody checked.
  tmp=$(mktemp -d)
  printf 'anything\n' >"$tmp/subject"

  # A regex awk cannot parse. If the helper still reports "absent", it is
  # reporting its own failure as a finding about the file.
  if assert_file_lacks_pattern '*(' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: an unparseable pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  # And the two honest outcomes still work, or the guard above is bought by
  # breaking the helper for everything.
  assert_file_lacks_pattern 'nothinglikethis' "$tmp/subject"
  if assert_file_lacks_pattern 'anything' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: a present pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "the counters are read from the kubelet of the node the worker runs on" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # The endpoint is per node and the counters are per container, so the node
  # name has to come from the Pod. Reading a fixed node, or the wrong one,
  # returns a healthy stranger's counters.
  assert_file_contains 'get --raw /api/v1/nodes/srv1/proxy/metrics/cadvisor' "$kubectl_calls"
  # And the node name comes from the field this caller asks the listing for.
  # That projection is a parameter of the shared listing, and the other caller
  # of it passes the Pod name instead; swapped, this capture would ask kubelets
  # named after Pods and get nothing, with no error to say why. The mock answers
  # any listing regardless of what was asked for, so nothing else here sees it.
  assert_file_contains 'jsonpath={range .items[*]}{.spec.nodeName}{"\n"}{end}' "$kubectl_calls"
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'container_cpu_cfs_throttled_periods_total' "$capture"
  # One assertion per alternative in the filter's regex, because a typo in an
  # alternative no test drives through drops that family silently while every
  # other assertion here stays green. The scheduled-periods count is the
  # denominator the throttled count is read against, and the duration is what
  # says whether a small ratio was a long stall.
  assert_file_contains 'container_cpu_cfs_periods_total' "$capture"
  assert_file_contains 'container_cpu_cfs_throttled_seconds_total' "$capture"
  # The negative half of the labels. A complete capture that tells the reader
  # its answer is missing, while the answer sits in the same file, is the same
  # "capture misreads itself" harm as silence reported as an answer -- just
  # inverted, and it fires on every healthy run rather than on the rare one.
  assert_file_lacks_pattern 'incomplete' "$capture"
  assert_file_lacks_pattern 'unknown' "$capture"
  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  [ ! -f "$dir/srv1.read-error.log" ]
  [ ! -f "$dir/READ-WARNINGS.txt" ]
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  # The in-tree name the stream falls back to when mktemp fails. The live
  # cleanup pin is "the raw stream is removed after each node", which follows
  # the stream to TMPDIR; this one only says the fallback leaves nothing behind
  # either, and it would go green on a run that never took the fallback.
  [ ! -f "$dir/srv1.stream.tmp" ]
  rm -rf "$tmp"
}

@test "what the workers consumed is captured beside what they were denied" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # The throttled counters say the container met its ceiling. They cannot say
  # how much CPU it got, and the two are different quantities: a guest held at a
  # one-core ceiling and a guest scheduled onto a physical CPU for a tenth of
  # that ceiling both report throttled periods, and the ratio alone does not
  # separate them. The consumed seconds are what does, and cAdvisor publishes
  # them on the same stream this capture already reads, so the answer costs a
  # wider filter rather than another read.
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'container_cpu_usage_seconds_total' "$capture"
  rm -rf "$tmp"
}

@test "each read is bounded and keeps its exit code" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # An unbounded read here would hold the whole diagnostics block against a
  # wedged kubelet, and the phase budget bounds when a read may START, never
  # how long one already running may take.
  # Named by their verbs, not by the bound: both reads carry the same one, so a
  # bare '-k 5 20 kubectl' would be satisfied twice by whichever one survived
  # and would not notice the other losing its ceiling.
  # The selector travels with the listing. Without it the walk covers every Pod
  # in the namespace, and the node set becomes every node running anything.
  assert_file_contains '-k 5 20 kubectl -n tenant-test get pods -l kubevirt.io=virt-launcher' "$timeout_calls"
  assert_file_contains '-k 5 20 kubectl get --raw' "$timeout_calls"
  # Both bounds, not one. The wall-clock wrapper bounds the process and
  # --request-timeout bounds the HTTP request underneath it; the second is the
  # only one left when `timeout` is not on PATH, which is the case the fallback
  # below exists for.
  assert_file_contains '--request-timeout=20s' "$kubectl_calls"
  assert_file_contains '[capture exit code: 0]' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  rm -rf "$tmp"
}

@test "the sample number decides which reading a file belongs to" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  ( set +x; cozy_capture_tenant_worker_cpu_throttle 2 )

  # Every counter here is cumulative since the container started, so one reading
  # is an average over an uptime and the pair is what gives a rate over the
  # window the deadline covered. A second reading written over the first leaves
  # one file and no interval -- collected, and unable to answer the question it
  # was collected for.
  assert_file_contains 'container_cpu_usage_seconds_total' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-2/srv1.txt"
  [ ! -d "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1" ]
  rm -rf "$tmp"
}

@test "each reading records when it was taken" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # The knob names how long the block WAITS between its two passes, which is not
  # the interval a subject's two readings span: the other subject's pass falls
  # between them, and on the run this collector exists for that pass is the slow
  # part. A reader who divides by the advertised number is then wrong by
  # whatever that pass cost, in the direction that understates a rate. The stamp
  # is what makes the real interval readable off the pair.
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'read attempted from' "$capture"
  # Both ends, not one. A single stamp leaves the sampling instant somewhere
  # inside a read whose duration is what goes wrong on the run this collector
  # exists for, so the interval a reader divides by would be off by up to the
  # read bound with nothing in the artifact saying so.
  stamp=$(sed -n 's/.*read attempted from \([0-9][0-9]*\) to \([0-9][0-9]*\) epoch seconds.*/\1 \2/p' "$capture")
  if [ -z "$stamp" ]; then
    echo "expected a bare epoch stamp in $capture" >&2
    cat "$capture" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the raw stream is removed after each node" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1 srv2"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # The stream is staged outside the report tree, so the assertion that used to
  # catch a missing cleanup -- "no .stream.tmp in the report dir" -- cannot see
  # it any more. Point TMPDIR at a directory of our own and check it is empty
  # afterwards: mktemp honours TMPDIR, so this pins the cleanup wherever the
  # file is staged, rather than pinning the path it used to have.
  scratch="$tmp/scratch"
  mkdir -p "$scratch"

  TMPDIR="$scratch" run_capture

  left=$(find "$scratch" -type f | wc -l | tr -d ' ')
  if [ "$left" -ne 0 ]; then
    echo "expected the raw metric stream to be removed; $left file(s) left in TMPDIR" >&2
    find "$scratch" -type f >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "a lowered read budget moves both of this collector's bounds" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # Assigned after sourcing, which is how both a caller and a test set it. A
  # collector that took the knob's value at source time would keep 20 here.
  #
  # Not restored afterwards, and deliberately: run_capture calls the collector
  # in an explicit subshell, and both runners give every @test a subshell of its
  # own, so the assignment cannot outlive this body in either direction. A reset
  # on the last line would be dead code that reads as load-bearing cleanup and
  # teaches the next reader that state crosses tests here.
  COZY_DIAG_READ_TIMEOUT=7

  run_capture

  assert_file_contains '-k 5 7 kubectl -n tenant-test get pods' "$timeout_calls"
  assert_file_contains '-k 5 7 kubectl get --raw' "$timeout_calls"
  assert_file_contains '--request-timeout=7s' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a zero read budget is corrected rather than disabling the bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # Zero is the value that makes revalidation load-bearing rather than tidy:
  # `timeout -k 5 0` disables the timeout outright and `--request-timeout=0s`
  # means no timeout to kubectl, so a zero assigned after sourcing restores the
  # unbounded read on exactly the wedged-kubelet path the bound exists for.
  COZY_DIAG_READ_TIMEOUT=0

  run_capture

  assert_file_contains '-k 5 20 kubectl get --raw' "$timeout_calls"
  # Bracketed rather than backslash-escaped: awk warns on `\-`, and a warning
  # nobody reads is how an unparseable pattern would arrive here.
  assert_file_lacks_pattern '[-]k 5 0 ' "$timeout_calls"
  assert_file_lacks_pattern 'request-timeout=0s' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the read's own status survives the filter that follows it" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # A kubelet that answered in full, whose body carries nothing for this
  # namespace. Piped straight into grep the collector would report the read's
  # status as grep's and call a healthy read a failure -- and, the other way
  # round, would report a dead kubelet as a node with no tenant container.
  kubectl_raw_output='container_cpu_cfs_periods_total{container="x",namespace="kube-system",pod="p"} 7'
  kubectl_raw_rc=0
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains '[capture exit code: 0]' "$capture"
  assert_file_contains 'the kubelet answered' "$capture"
  assert_file_lacks_pattern 'the kubelet was not read' "$capture"
  rm -rf "$tmp"
}

@test "a node whose read times out does not stop the next node" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1 srv2"
  timeout_fail_node=srv1
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-partial

  run_capture

  # One throttled worker answers the question, but only if the walk survives
  # the node that did not answer -- and the wedged one is as likely as not to
  # be first, since the node list is sorted.
  dir="$COZY_REPORT_DIR/snapshots/throttle-partial/tenant-cpu-throttle/sample-1"
  assert_file_contains '[capture exit code: 124]' "$dir/srv1.txt"
  assert_file_contains '[capture exit code: 0]' "$dir/srv2.txt"
  rm -rf "$tmp"
}

@test "a warning on a successful listing is kept apart from a setup failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_list_stderr='Warning: v1 ComponentStatus is deprecated'
  kubectl_list_rc=0
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # kubectl writes warnings on stderr with a zero exit, so the filename is
  # decided by the status rather than by something having been written. Filed
  # as a collection failure, a healthy listing reads as a collector that never
  # ran at all.
  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  assert_file_contains 'ComponentStatus is deprecated' "$dir/READ-WARNINGS.txt"
  [ ! -f "$dir/COLLECTION-FAILED.txt" ]
  assert_file_contains 'container_cpu_cfs_throttled_periods_total' "$dir/srv1.txt"
  rm -rf "$tmp"
}

@test "a pool below the cap leaves no truncation marker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1 srv2"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-uncapped

  run_capture

  [ ! -f "$COZY_REPORT_DIR/snapshots/throttle-uncapped/tenant-cpu-throttle/sample-1/COLLECTION-TRUNCATED.txt" ]
  rm -rf "$tmp"
}

@test "repeated nodes are asked once" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  # Two workers co-scheduled on one node is the ordinary case, not a corner:
  # the endpoint is per node and carries both, so asking twice spends a second
  # read out of a budget that declines whatever has not started, and buys a
  # byte-for-byte duplicate.
  kubectl_node_names="srv1 srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  count=$(grep -c 'get --raw' "$kubectl_calls" || true)
  if [ "$count" -ne 1 ]; then
    echo "expected one kubelet read for one node, got $count" >&2
    return 1
  fi
  rm -rf "$tmp"
}

@test "the collector's own caps stay what its comments claim" {
  # Values, not sums. A comment stating a total has to be kept true in every
  # copy of it and falsifies silently; a cap the code carries as a literal can
  # be pinned against the code itself, which is what this does.
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The walk and the read moved into the body this capture now shares with the
  # network counters, so the subject of these assertions is the code the
  # collector runs rather than the function that carries its name -- which is
  # now a six-line call. Reading only the named function would leave every
  # assertion below vacuously true against a body it no longer contains.
  fn=$(awk '/^cozy_capture_tenant_worker_cpu_throttle\(\)/,/^}/' "$lib"
      awk '/^_cozy_capture_worker_cadvisor\(\)/,/^}/' "$lib"
      awk '/^_cozy_cadvisor_node_stream\(\)/,/^}/' "$lib"
      awk '/^_cozy_virt_launcher_listing\(\)/,/^}/' "$lib")
  case "$fn" in
    *'max_nodes=3'*) ;;
    *) echo "expected the collector to cap the walk at the 3 nodes the comments state" >&2; return 1 ;;
  esac
  # The knob, not a literal that equals it today. A hardcoded 20 keeps its
  # value when the block's budget is lowered, so the one read that ignores the
  # knob is the one that outlives it -- and the sibling suite that lowers the
  # knob is what caught exactly that here.
  case "$fn" in
    *'timeout -k "${COZY_DIAG_READ_GRACE}" "${COZY_DIAG_READ_TIMEOUT}"'*) ;;
    *) echo "expected each kubelet read to take the block's read-budget knob" >&2; return 1 ;;
  esac
  case "$fn" in
    *'timeout -k 5 20'*) echo "expected no hardcoded bound to survive beside the knob" >&2; return 1 ;;
  esac
  # Checked in the source as well as exercised, not instead of it. `command -v`
  # finds the shell function this file defines for `timeout`, so the fallback arm
  # cannot be reached from a test body -- but it can be reached from a subprocess
  # with a stripped PATH, and a test further down does exactly that.
  # The scratch stream must not live in the report tree. This is a source-level
  # claim on purpose: the case it guards against is a hard kill between writing
  # the stream and removing it, which no unit test can stage, so the only thing
  # that can be pinned is where the path points.
  #
  # Matched against CODE, with comment lines stripped first. The prose here
  # names both `mktemp` and the fallback path, so a pattern run over the whole
  # function is satisfied by the explanation of the rule instead of by the rule
  # -- deleting the assignment outright would leave such a check green.
  code=$(printf '%s\n' "$fn" | grep -v '^[[:space:]]*#')
  case "$code" in
    *'stream=$(mktemp "'*) ;;
    *) echo "expected the raw metric stream to be staged via mktemp with an explicit template" >&2; return 1 ;;
  esac
  # The template must be explicit: BSD mktemp with no template ignores TMPDIR
  # and always lands in the system directory, so a bare call puts the file
  # somewhere neither this suite nor an operator chose.
  case "$code" in
    *'mktemp "${TMPDIR:-/tmp}/'*) ;;
    *) echo "expected the scratch template to honour TMPDIR explicitly" >&2; return 1 ;;
  esac
  # report_dir may appear only as the fallback, never as the primary target.
  primary=$(printf '%s\n' "$code" | grep 'stream=' | grep -v '^[[:space:]]*||' | head -n 1)
  case "$primary" in
    *'${report_dir}'*) echo "the raw stream must not be staged inside the report tree" >&2; return 1 ;;
  esac
  # Both reads, not just the kubelet one: the listing has the same two arms and
  # the same fallback, so counting only `get --raw` leaves the arm that matters
  # for the listing unwatched for exactly the same reason.
  # This loop is the cheap half of that pair: it holds every read that EXISTS to
  # the flag. It cannot see a branch that was deleted, because a count over the
  # reads still present balances either way -- that is what the stripped-PATH
  # test buys, and why both are here.
  for verb in 'kubectl get --raw' 'kubectl -n tenant-test get pods'; do
    reads=$(printf '%s\n' "$fn" | grep -c "$verb" || true)
    bounded=$(printf '%s\n' "$fn" | grep -A3 "$verb" | grep -c -- '--request-timeout=' || true)
    if [ "$reads" -eq 0 ] || [ "$reads" -ne "$bounded" ]; then
      echo "every '$verb' must carry --request-timeout; $bounded of $reads do" >&2
      return 1
    fi
  done
}

@test "both chainsaw comments list the collector where the phase actually spends it" {
  # The ORDER is the claim, not any duration. Those comments describe the order
  # the phase budget is spent in, and a budget that runs out declines whatever
  # has not started -- so an entry in the wrong place misdescribes which
  # collector survives a tight run. Pinning a minute figure instead would pin
  # the one thing in that comment nobody can keep true.
  for f in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
           hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
    reads=$(grep -n 'node-join diagnostics reads' "$f" | head -n 1 | cut -d: -f1)
    cgroup=$(grep -n 'worker CPU usage and throttling counters' "$f" | head -n 1 | cut -d: -f1)
    console=$(grep -n 'serial-console family' "$f" | head -n 1 | cut -d: -f1)
    talos=$(grep -n 'guest Talos capture' "$f" | head -n 1 | cut -d: -f1)
    if [ -z "$reads" ] || [ -z "$cgroup" ] || [ -z "$console" ] || [ -z "$talos" ]; then
      echo "expected $f to list the reads, the counter capture, the console family and the guest Talos capture" >&2
      return 1
    fi
    # Below the console, not above it. This capture is read twice and can spend
    # most of the phase budget, and admission gates only when a collector may
    # start, so above the console it would bound what the console gets -- and
    # the console is the only surface that survives a worker which never reached
    # apid. Above the guest Talos capture, which needs the apid that failure
    # never reached and is the more expensive of the two.
    if [ "$cgroup" -le "$console" ] || [ "$cgroup" -ge "$talos" ]; then
      echo "in $f the counter capture must sit between the console family and the guest Talos capture" >&2
      return 1
    fi
    if [ "$console" -le "$reads" ]; then
      echo "in $f the console family must sit after the node-join reads" >&2
      return 1
    fi
  done
}

@test "the capture is declined by the phase budget rather than running unconditionally" {
  # Ungated, this collector would spend its budget after the phase had already
  # declined the cheaper reads around it -- which is the failure the phase
  # budget exists to prevent, reintroduced by the newest caller.
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  gate=$(grep -n "^ *if cozy_diag_phase_has_time '(d) tenant worker CPU counters, sandbox node CPU time and worker per-thread CPU time'; then$" \
    "$lib" | head -n 1 | cut -d: -f1)
  call=$(grep -n '^ *cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$gate" ] || [ -z "$call" ]; then
    echo "expected the capture to sit behind a cozy_diag_phase_has_time gate" >&2
    return 1
  fi
  # THIS gate, not just some gate above the call. A line comparison alone is
  # satisfied by any earlier gate in the block, which would leave the capture
  # guarded by a decision taken about something else. The call now sits inside
  # the sampling loop rather than on the line after the gate, so adjacency is no
  # longer the way to say it: what says it is that no other gate opens in
  # between.
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

@test "the capture runs ahead of the collectors that cost more, and behind the one it could starve" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  call=$(grep -n '^ *cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$call" ]; then
    echo "expected the failure path to call the throttling capture" >&2
    return 1
  fi
  # What runs last is what the budget declines, so position is what decides
  # whether this collector is ever taken on a tight run. Its question has no
  # other answer in this tree, so it precedes the legs below. Stated without a
  # count on purpose: the loop below is the enumeration, and a sentence that
  # also counted would go stale the first time a leg is added to it.
  for leg in 'cozy_capture_tenant_talos "${test_name}" || true' \
             'ghcr_mirror_diagnose || true' \
             'talos_image_cache_diagnose || true'; do
    line=$(grep -n -F -x "    ${leg}" "$lib" | head -n 1 | cut -d: -f1)
    if [ -z "$line" ]; then
      echo "expected the failure block to still call: ${leg}" >&2
      return 1
    fi
    if [ "$call" -ge "$line" ]; then
      echo "the throttling capture (line $call) must run before ${leg} (line $line)" >&2
      return 1
    fi
  done
  # And behind the console. Reading the counters twice costs the walk twice
  # plus the interval, which is most of the phase budget at the read bound and
  # the node cap, and admission gates only when a collector may START. Ahead of
  # the console, a run that hits those bounds would leave the console never
  # started rather than cut short -- and the console is the only capture that
  # survives a worker which never reached apid, the shape this failure usually
  # takes. Cost, not worth, is what puts it here.
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$console" ]; then
    echo "expected the failure block to still capture the guest serial console" >&2
    return 1
  fi
  if [ "$call" -le "$console" ]; then
    echo "the throttling capture (line $call) must run after the console capture (line $console)" >&2
    return 1
  fi
}

@test "the ceiling is captured beside the counters" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  # The throttled counters on their own say a container hit some ceiling, not
  # which one. Without the quota beside them the reader cannot tell a VM capped
  # at one core from one capped at eight, which is the whole question.
  assert_file_contains 'container_spec_cpu_quota' "$capture"
  # The period travels with the quota because the quota alone is meaningless:
  # 100000us of quota is one core against a 100000us period and a tenth of one
  # against a 1000000us period.
  assert_file_contains 'container_spec_cpu_period' "$capture"
  rm -rf "$tmp"
}

@test "the capture tells the reader how to pair it with its sibling sample" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # The instruction lives with the subject that has a sibling rather than in the
  # walk both subjects share, because the other caller of that walk takes one
  # sample and would otherwise carry an instruction pointing at a directory that
  # is never created.
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'subtract this node file from its sibling under the other sample directory' "$capture"
  rm -rf "$tmp"
}

@test "a read that did not finish is not told to pair with a sibling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # Series arrived and the read still failed, which is a stream cut off part way
  # through.
  kubectl_raw_rc=124

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'these counters are incomplete' "$capture"
  # Telling a reader to subtract two files asserts that both hold a whole
  # reading. This one is already marked incomplete, and a difference taken
  # against it understates the counter by whatever the read did not return --
  # in the direction that makes a starved worker look busier than it was. The
  # sandbox capture withholds its column legend from a short read for the same
  # reason, so the pair answers this the same way on both sides.
  assert_file_lacks_pattern 'subtract this node file from its sibling' "$capture"
  rm -rf "$tmp"
}

@test "an unquota'd container is reported as uncapped rather than as a gap" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # One line, and it is the whole shape: cAdvisor gates the quota series and all
  # three CFS counters on the same non-zero quota, so an uncapped container puts
  # the period on the wire and nothing else. Staging a CFS counter beside it
  # would be a shape the endpoint cannot produce, and the test would then pass
  # on a fixture rather than on the contract. It is an answer, not a short read:
  # filed as "nothing was collected" it would send the reader looking for a
  # ceiling that does not exist.
  kubectl_raw_output='container_spec_cpu_period{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 100000'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'container_spec_cpu_period' "$capture"
  assert_file_lacks_pattern 'unknown' "$capture"
  assert_file_lacks_pattern 'incomplete' "$capture"
  # Said in words, not left to inference. One line at exit 0 is also what a
  # truncated read looks like, and every other outcome in this collector gets a
  # sentence -- so the one answer it was built to deliver cannot be the one that
  # gets none.
  assert_file_contains 'running uncapped' "$capture"
  rm -rf "$tmp"
}

@test "a quota probe that could not read says nothing about the ceiling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # The last grep in the function is the one that decides whether the artifact
  # makes a positive claim about the ceiling. grep exits 2 when it cannot read,
  # and a bare `!` turns that into "no quota found", which is the direction of
  # error this whole collector exists to refuse: a statement about the cluster
  # derived from a read that failed on this runner.
  grep() {
    case "$*" in
      *'^container_spec_cpu_quota{'*) return 2 ;;
    esac
    command grep "$@"
  }

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_lacks_pattern 'running uncapped' "$capture"
  unset -f grep
  rm -rf "$tmp"
}

@test "a capped container is not labelled uncapped" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # The default fixture is the capped shape: every family, quota included.
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'container_spec_cpu_quota' "$capture"
  assert_file_lacks_pattern 'running uncapped' "$capture"
  rm -rf "$tmp"
}

@test "a read killed by the grace signal says so rather than leaving the code alone" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=137
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # 137 is what `timeout -k` leaves behind when the child ignored SIGTERM and
  # had to be killed, by whatever did it. Naming every way a read comes up
  # short is this collector's whole argument,
  # so the one status that means "killed, not finished" cannot be the exception.
  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'exit 137' "$capture"
  assert_file_lacks_pattern 'exit 124' "$capture"
  rm -rf "$tmp"
}

@test "a kubelet that was not read is recorded as unknown, not as an absence of throttling" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=1
  kubectl_raw_output=
  kubectl_raw_stderr='error: unable to connect to the server'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  # This is the failure this collector exists to prevent, inverted: an empty
  # file here reads as a container that never hit its ceiling, which is the
  # exact conclusion the capture was added to stop people reaching by default.
  assert_file_contains 'unknown' "$capture"
  # `unknown` alone is shared by both labels, so it pins the word and not the
  # branch: disable this arm and an unread kubelet falls through to the next
  # one, where the artifact tells the reader the kubelet ANSWERED and merely
  # carried nothing. Pin the sentence that only this arm writes.
  assert_file_contains 'the kubelet was not read' "$capture"
  # And that it points at the log rather than leaving the reader to find it.
  assert_file_contains 'see read-error.log' "$capture"
  # The stamp claims only when the read was attempted. This arm has just said
  # nothing was read, so a line asserting the counters were sampled inside the
  # bracket contradicts the line above it -- the same two-lines-disagreeing
  # failure the pairing note and the legend are both gated against.
  assert_file_lacks_pattern 'counters were sampled' "$capture"
  assert_file_contains 'read attempted from' "$capture"
  assert_file_contains 'error: unable to connect to the server' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.read-error.log"
  rm -rf "$tmp"
}

@test "a forbidden nodes-proxy read is preserved rather than folded into silence" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=1
  kubectl_raw_output=
  # RBAC on nodes/proxy is a failure mode this read has and an in-container
  # read did not. It is also the one an operator can fix, so the 403 has to
  # reach the artifact verbatim instead of becoming a generic "not read".
  kubectl_raw_stderr='Error from server (Forbidden): nodes "srv1" is forbidden: User "system:serviceaccount:x:y" cannot get resource "nodes/proxy"'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  assert_file_contains 'the kubelet was not read' "$dir/srv1.txt"
  assert_file_contains 'nodes/proxy' "$dir/srv1.read-error.log"
  assert_file_contains 'Forbidden' "$dir/srv1.read-error.log"
  rm -rf "$tmp"
}

@test "a read killed without a word is not reported as a kubelet that answered" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # `timeout` kills its child and writes nothing, and kubectl dies on SIGTERM
  # without a word, so this -- not the tidy error above -- is the shape a
  # wedged kubelet actually produces. Keyed on whether stderr exists rather
  # than on the status, the collector calls it an answer.
  kubectl_raw_rc=124
  kubectl_raw_output=
  kubectl_raw_stderr=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'the kubelet was not read' "$capture"
  # The half that makes it a defect rather than a wording preference: the
  # artifact must not state the opposite of what happened.
  assert_file_lacks_pattern 'the kubelet answered' "$capture"
  # No error log exists for a silent kill, so pointing at one sends the reader
  # after evidence that was never written.
  assert_file_lacks_pattern 'see read-error.log' "$capture"
  assert_file_contains 'without a word' "$capture"
  [ ! -f "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.read-error.log" ]
  rm -rf "$tmp"
}

@test "a stream cut short without a word is still marked incomplete" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # Counters arrived, the ceiling did not, and the read died silently. This is
  # the worst shape in the set: the header states that counters without a quota
  # mean a container running uncapped, so an unlabelled truncation does not
  # merely lose information -- it reads as a positive finding about the ceiling.
  kubectl_raw_output='container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 4211'
  kubectl_raw_rc=137
  kubectl_raw_stderr=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'incomplete' "$capture"
  assert_file_lacks_pattern 'see read-error.log' "$capture"
  rm -rf "$tmp"
}

@test "a filter that cannot read the stream blames neither the kubelet nor the namespace" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # grep exits 2 when it cannot read its input -- a full TMPDIR on the runner
  # reaches this. The kubelet read itself succeeded, so the artifact must not
  # say the kubelet answered and carried nothing (that is the conflation this
  # collector exists to prevent), and must not say the kubelet was not read
  # (it was). It has to name the local failure and nothing else.
  grep() { case "$*" in *"-E "*) command grep "$@" >/dev/null 2>&1; return 2 ;; esac; command grep "$@"; }

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'could not be read back on this runner' "$capture"
  assert_file_lacks_pattern 'the kubelet answered' "$capture"
  assert_file_lacks_pattern 'the kubelet was not read' "$capture"
  unset -f grep
  rm -rf "$tmp"
}

@test "a filter failure that said why points at the log it wrote" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # The half of this branch the sibling tests do not reach: they all silence the
  # filter's stderr, so all of them land in the wordless variant and none drives
  # the one that names the log. Both variants open with the same sentence, so a
  # test asserting only that sentence passes on either.
  grep() {
    case "$*" in
      *"-E "*) echo 'grep: input: Input/output error' >&2; return 2 ;;
    esac
    command grep "$@"
  }

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  assert_file_contains 'could not be read back on this runner' "$dir/srv1.txt"
  assert_file_contains 'see filter-error.log' "$dir/srv1.txt"
  assert_file_lacks_pattern 'the filter said nothing about why' "$dir/srv1.txt"
  assert_file_contains 'Input/output error' "$dir/srv1.filter-error.log"
  unset -f grep
  rm -rf "$tmp"
}

@test "a filter failure never names a log that was not written" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # Fails silently: exit 2 with nothing on stderr. Naming a file that was never
  # created sends the reader after evidence that does not exist -- the same rule
  # every other message in this function follows.
  grep() { case "$*" in *"-E "*) return 2 ;; esac; command grep "$@"; }

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  assert_file_contains 'the filter said nothing about why' "$dir/srv1.txt"
  assert_file_lacks_pattern 'see filter-error.log' "$dir/srv1.txt"
  [ ! -f "$dir/srv1.filter-error.log" ]
  unset -f grep
  rm -rf "$tmp"
}

@test "a namespace filter that cannot read its input is not reported as an answered kubelet" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # The same unreadable-input failure as the metric filter above, one stage
  # later. Every stage of the filter narrows the same stream, so an exit 2 from
  # any of them leaves the capture empty for a reason that has nothing to do
  # with what the kubelet said.
  grep() { case "$*" in *'namespace="tenant-test"'*) return 2 ;; esac; command grep "$@"; }

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'could not be read back on this runner' "$capture"
  assert_file_lacks_pattern 'the kubelet answered' "$capture"
  assert_file_lacks_pattern 'the kubelet was not read' "$capture"
  unset -f grep
  rm -rf "$tmp"
}

@test "a worker filter that cannot read its input is not reported as an answered kubelet" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # A chain that pipes the stage above into this one drops that stage's status
  # at the pipe and this one's at the trailing `|| true`, so neither reaches the
  # artifact and both need their own test: one of them passing says nothing
  # about the other.
  grep() { case "$*" in *'pod="virt-launcher-'*) return 2 ;; esac; command grep "$@"; }

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'could not be read back on this runner' "$capture"
  assert_file_lacks_pattern 'the kubelet answered' "$capture"
  assert_file_lacks_pattern 'the kubelet was not read' "$capture"
  unset -f grep
  rm -rf "$tmp"
}

@test "a filter that fails after writing leaves the capture marked incomplete" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  # A write error is where grep emits what it had flushed and then exits 2 --
  # ENOSPC on a full disk reaches it, and so does the full TMPDIR this split was
  # written for. The counters that arrive are real, so the capture is not
  # unknown; it is short, and the family that goes missing first is the quota,
  # whose absence reads as a container running uncapped.
  grep() {
    case "$*" in
      *'pod="virt-launcher-'*)
        command grep "$@" | head -n 1
        echo 'grep: stdout: File too large' >&2
        return 2
        ;;
    esac
    command grep "$@"
  }

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'incomplete' "$capture"
  assert_file_contains 'see filter-error.log' "$capture"
  # The read itself was fine, so nothing here may blame it.
  assert_file_lacks_pattern 'the read was cut short' "$capture"
  assert_file_lacks_pattern 'see read-error.log' "$capture"
  unset -f grep
  rm -rf "$tmp"
}

@test "a filter that fails after writing never names a log that was not written" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke
  grep() {
    case "$*" in
      *'pod="virt-launcher-'*) command grep "$@" | head -n 1; return 2 ;;
    esac
    command grep "$@"
  }

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  assert_file_contains 'incomplete' "$dir/srv1.txt"
  assert_file_contains 'the filter said nothing about why' "$dir/srv1.txt"
  assert_file_lacks_pattern 'see filter-error.log' "$dir/srv1.txt"
  [ ! -f "$dir/srv1.filter-error.log" ]
  unset -f grep
  rm -rf "$tmp"
}

@test "a namespace whose containers are none of them workers names the worker filter" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=0
  # Series for this namespace, none of them from a worker Pod -- what an
  # upstream rename of the virt-launcher prefix looks like from here. Saying the
  # kubelet reported nothing for the namespace would be false in this state and
  # would send a reader after scheduling while the filter is what emptied the
  # capture.
  kubectl_raw_output='container_cpu_cfs_throttled_periods_total{container="kube-apiserver",namespace="tenant-test",pod="kube-apiserver-a"} 7
container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="RENAMED-launcher-worker-a"} 4211'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'unknown' "$capture"
  assert_file_contains 'none of them from a worker Pod' "$capture"
  assert_file_lacks_pattern 'reported no CPU series' "$capture"
  assert_file_lacks_pattern 'the kubelet was not read' "$capture"
  # The rejected series must not ride along either: naming the filter is not a
  # licence to print what it rejected.
  assert_file_lacks_pattern 'kube-apiserver' "$capture"
  rm -rf "$tmp"
}

@test "a kubelet that answered without this namespace is not reported as unread" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=0
  kubectl_raw_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  # Distinct from the kubelet that never answered: this one did, and an
  # artifact that blames the read would send an operator after RBAC and
  # networking when the actual finding is that no worker container was there.
  assert_file_contains 'unknown' "$capture"
  assert_file_contains 'the kubelet answered' "$capture"
  assert_file_lacks_pattern 'see read-error.log' "$capture"
  # Both empty-capture arms open with "the kubelet answered", so the line above
  # cannot tell them apart on its own. This one is about a node carrying nothing
  # of the namespace, and the sibling arm's wording is what it must not be.
  assert_file_contains 'no CPU series for a tenant-test worker' "$capture"
  assert_file_lacks_pattern 'none of them from a worker Pod' "$capture"
  rm -rf "$tmp"
}

@test "with timeout off PATH the kubelet read still runs and keeps its request bound" {
  # The fallback arm is what the phase-start warning promises for this collector:
  # on a runner without the binary it runs UNBOUNDED rather than exiting 127, and
  # `--request-timeout` is then the only ceiling left. A promise the code does not
  # keep is what this whole collector is against, so it is entered here rather
  # than inspected.
  #
  # A subprocess with a stripped PATH, because this file mocks `timeout` as a
  # shell function and `command -v` finds functions -- the condition cannot be
  # reached from inside a test body. Same device as the sibling suite uses for
  # the block's own reads.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  # Everything the collector shells out to, minus `timeout` itself. Missing one
  # would fail this test for the wrong reason and read as the arm being broken.
  for c in mkdir sort grep mv rm mktemp wc tr cat date; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  for c in mkdir sort grep mv rm mktemp wc tr date; do
    if [ ! -x "$tmp/bin/$c" ]; then
      echo "FAIL: could not stage $c in the stripped PATH; the check below would be vacuous" >&2
      return 1
    fi
  done
  # Only the staged directory is checked. `command -v timeout` is always true
  # here -- this file defines it as a shell function, which is the very fact the
  # subprocess exists to escape -- so folding it in would read as if both halves
  # mattered while only this one does.
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
      if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then printf "srv1\n"; return 0; fi
      if [ "${1:-}" = get ] && [ "${2:-}" = --raw ]; then
        printf "container_cpu_cfs_throttled_periods_total{container=\"compute\",namespace=\"tenant-test\",pod=\"virt-launcher-a\"} 7\n"
        return 0
      fi
      return 0
    }
    CALLS='"$tmp"'/calls
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=fallback
    PATH='"$tmp"'/bin
    cozy_capture_tenant_worker_cpu_throttle 1
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/out"

  # The read ran at all -- bounded into exit 127 would leave nothing here.
  assert_file_contains 'KUBECTL get --raw' "$tmp/calls"
  # And carried the only bound it still has. This is the assertion the source
  # check cannot make: it proves the flag reaches the arm that is actually taken.
  assert_file_contains '--request-timeout=20s' "$tmp/calls"
  # And produced counters rather than an "unknown" label.
  assert_file_contains 'container_cpu_cfs_throttled_periods_total' \
    "$tmp/report/snapshots/fallback/tenant-cpu-throttle/sample-1/srv1.txt"
  rm -rf "$tmp"
}

@test "a killed read is not written up as a grace period that never ran" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  for c in mkdir sort grep mv rm mktemp wc tr cat date; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  if [ ! -x "$tmp/bin/grep" ]; then
    echo "could not stage a PATH without timeout" >&2
    return 1
  fi

  # 137 on the arm that has no `timeout` at all. The status is 128+SIGKILL and
  # says only that something killed the read -- an OOM killer on a loaded
  # runner, a teardown signalling the process group. On this arm the grace
  # period is the one cause it cannot have been, because there is no `timeout`
  # in the call. cozy_diag_read refuses the same wording for the same reason.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    kubectl() {
      if [ "${3:-}" = get ] && [ "${4:-}" = pods ]; then printf "srv1\n"; return 0; fi
      if [ "${1:-}" = get ] && [ "${2:-}" = --raw ]; then return 137; fi
      return 0
    }
    COZY_REPORT_DIR='"$tmp"'/report
    COZY_SNAPSHOT_NAME=killed
    PATH='"$tmp"'/bin
    cozy_capture_tenant_worker_cpu_throttle 1
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/out"

  capture="$tmp/report/snapshots/killed/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'exit 137' "$capture"
  assert_file_lacks_pattern 'grace period' "$capture"
  rm -rf "$tmp"
}

@test "the tenant control plane is not reported as a worker" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # tenant-test is not a worker-only namespace. It also carries the Kamaji
  # control plane -- the same file reads the talos-csr-signer sidecar out of it
  # -- and the CDI importer Pods. Those containers carry their own CPU limits,
  # so they have a real CFS quota and can show genuine throttling. Reported
  # under a heading that says "worker", a throttled apiserver answers the
  # question with the wrong subject, which is the harm the neighbouring test
  # guards against from a different namespace -- and this direction is likelier.
  kubectl_raw_output='container_cpu_cfs_throttled_periods_total{container="kube-apiserver",namespace="tenant-test",pod="kubernetes-test-latest-version-abc"} 9999
container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-worker-a-11111"} 4211'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'virt-launcher-worker-a-11111' "$capture"
  assert_file_lacks_pattern 'kube-apiserver' "$capture"
  rm -rf "$tmp"
}

@test "another tenant's worker on the same node is not reported as ours" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # The node filter is per node, and a management node hosts whatever is
  # scheduled on it. A virt-launcher belonging to a different tenant carries the
  # same Pod-name prefix and the same container, so the Pod filter alone cannot
  # separate them -- only the namespace can. Reported here it would be a
  # stranger's throttling presented as this suite's worker, which is the same
  # wrong-subject harm as the control plane, one namespace over.
  kubectl_raw_output='container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-other",pod="virt-launcher-elsewhere-99999"} 8888
container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-worker-a-11111"} 4211'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_contains 'virt-launcher-worker-a-11111' "$capture"
  assert_file_lacks_pattern 'tenant-other' "$capture"
  assert_file_lacks_pattern 'virt-launcher-elsewhere' "$capture"
  rm -rf "$tmp"
}

@test "a series that merely contains a metric name does not pass the filter" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # An unanchored match would take any series whose name embeds one of the
  # five, and the artifact would carry numbers that answer a different
  # question while looking like the ones that answer this one.
  # The Pod label has to match the worker filter, or the anchor is not what is
  # under test: without it these lines are dropped for the wrong reason and the
  # assertion holds whether the regex is anchored or not.
  kubectl_raw_output='xcontainer_cpu_cfs_periods_total{namespace="tenant-test",pod="virt-launcher-worker-a-11111"} 1
recorded_container_spec_cpu_quota{namespace="tenant-test",pod="virt-launcher-worker-a-11111"} 2'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  assert_file_lacks_pattern 'xcontainer' "$capture"
  assert_file_lacks_pattern 'recorded_container' "$capture"
  assert_file_contains 'the kubelet answered' "$capture"
  rm -rf "$tmp"
}

@test "a read status cannot be read as the guest's own answer" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_rc=124
  kubectl_raw_output=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  capture="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/srv1.txt"
  # `timeout` reports 124 on expiry and passes the command's own status through
  # otherwise, so a 124 here is either this collector's deadline or the read
  # exiting 124 by itself, and nothing separates them. The file states that
  # rather than picking one.
  assert_file_contains '124' "$capture"
  assert_file_contains 'cannot be told apart' "$capture"
  assert_file_contains '[capture exit code: 124]' "$capture"
  rm -rf "$tmp"
}

@test "the walk is capped and names both counts when it stops" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="n1 n2 n3 n4 n5 n6"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  # This runs inside a failure path that already spends minutes on other
  # collectors under one chainsaw op timeout. A short listing otherwise reads
  # as a small cluster rather than a cut walk.
  truncated="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/COLLECTION-TRUNCATED.txt"
  assert_file_contains '6 carried a worker in total' "$truncated"
  # n4, not n6: the cap's VALUE is the claim, and the two chainsaw Test files
  # order the failure path on it. Asserting some later node is absent leaves a
  # larger cap green, which would silently overspend the budget.
  [ -f "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/n3.txt" ]
  [ ! -f "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/n4.txt" ]
  rm -rf "$tmp"
}

@test "a read cut off part way through is not presented as a complete answer" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  # The counters arrived; the stream died before the spec families that carry
  # the ceiling. This is the one partial result that reads like a complete
  # answer, because a missing quota otherwise means "uncapped".
  kubectl_raw_output='container_cpu_cfs_throttled_periods_total{container="compute",namespace="tenant-test",pod="virt-launcher-a"} 4211'
  kubectl_raw_rc=1
  kubectl_raw_stderr='error: unexpected EOF'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  capture="$dir/srv1.txt"
  assert_file_contains 'incomplete' "$capture"
  assert_file_contains 'see read-error.log' "$capture"
  assert_file_contains 'unexpected EOF' "$dir/srv1.read-error.log"
  rm -rf "$tmp"
}

@test "a namespace with no scheduled worker says so instead of leaving an empty tree" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names=
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture || true

  # Distinct from the listing that never answered: this one got a real answer.
  # An unscheduled virt-launcher has no node, so there is no kubelet to ask --
  # which is itself a finding about the failure, not a gap in the collector.
  assert_file_contains 'no virt-launcher Pod with a node assigned' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a warning beside a healthy read is not filed as a failure" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="srv1"
  kubectl_raw_stderr='Warning: metrics endpoint is deprecated'
  kubectl_raw_rc=0
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture

  dir="$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1"
  # kubectl writes deprecation and partial-result warnings on stderr with a
  # zero exit, so the exit status decides the filename, not the fact that
  # something was written. Filed as an error, a healthy read would look failed.
  assert_file_contains 'deprecated' "$dir/srv1.READ-WARNINGS.txt"
  [ ! -f "$dir/srv1.read-error.log" ]
  assert_file_contains 'container_cpu_cfs_throttled_periods_total' "$dir/srv1.txt"
  rm -rf "$tmp"
}

@test "a Pod list that never returned is not reported as no workers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_list_rc=1
  kubectl_list_stderr='Unable to connect to the server'
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=throttle-smoke

  run_capture || true

  # An empty listing is what a cluster with no virt-launcher Pods produces. A
  # listing that never answered has to leave something else behind, and the
  # status it is judged on has to be kubectl's rather than that of whatever
  # post-processing follows it.
  assert_file_contains 'Unable to connect to the server' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/COLLECTION-FAILED.txt"
  assert_file_contains 'failed to list' \
    "$COZY_REPORT_DIR/snapshots/throttle-smoke/tenant-cpu-throttle/sample-1/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "the capture is wired into the node-join failure path" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The reporter is its own function now, so its declaration and its closing
  # brace bound the region. Anchoring on the `exit 1` that follows would measure
  # the caller instead: the exit sits outside this function, and every line of
  # the reporter would count as inside any span reaching it.
  head=$(grep -n '^cozy_report_node_join_failure() {$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$head" ]; then
    echo "expected the node-join failure reporter to still be a function" >&2
    return 1
  fi
  tail=$(awk -v h="$head" 'NR > h && $0 == "}" { print NR; exit }' "$lib")
  call=$(grep -n '^ *cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$call" ] || [ -z "$tail" ]; then
    echo "expected the node-join failure path to capture worker CPU throttling" >&2
    return 1
  fi
  # A collector nothing calls is the shape this tree has shipped before: it
  # reads as covered in review and produces nothing in the artifact. Pin the
  # call site, not just the function.
  if [ "$call" -le "$head" ] || [ "$call" -ge "$tail" ]; then
    echo "the throttling capture (line $call) must sit inside the reporter ($head..$tail)" >&2
    return 1
  fi
  # And that the reporter is still reached: a function nobody calls has the
  # same artifact as a collector nobody calls, one level up.
  grep -q '^ *cozy_report_node_join_failure "${test_name}"$' "$lib" || {
    echo "expected run_kubernetes_test to still call the node-join reporter" >&2
    return 1
  }
}
