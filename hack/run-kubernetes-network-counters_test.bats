#!/usr/bin/env bats
# Regression coverage for the tenant-worker network counter capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why the guest cannot answer it: a worker that
# misses the node-Ready deadline while its image pull crawls has two mechanisms
# behind the same guest-visible symptom. The link is slow, or the pull keeps
# restarting and discarding what it fetched. From inside the guest both read as
# a progress line that barely moves. The host-side byte counters separate them,
# because they count every byte that crossed into the Pod including the ones a
# restart threw away. cAdvisor reads these inside the Pod's network namespace
# and publishes one row per interface, and a bridge-bound worker has four --
# the number that answers is the renamed NIC's receive, while the interface
# still named eth0 is a dummy holding the Pod IP and reads zero. A capture that
# reports an unread kubelet as a quiet link would hand the next reader that same
# ambiguity while looking like it had settled it, so these tests pin the
# separation rather than the reading.
#
# The fixtures below are real kubelet output with the identifying labels
# rewritten, not lines composed from the metric documentation. That choice is
# the subject of two of the tests: the family carries no container name and is
# spelled packets_dropped, and both are invisible to anyone writing the filter
# from memory.

kubectl_calls=/dev/null
kubectl_node_names=
kubectl_list_rc=0
kubectl_list_stderr=
kubectl_raw_rc=0
kubectl_raw_stderr=

# One worker Pod's set as the kubelet serves it: eight families, one row each
# per interface, every one of them carrying container="" and cAdvisor's own
# sample timestamp as the third field. Trimmed to the labels the filter reads
# plus the ones that make the shape recognisable; the id/name digests are
# synthetic and the image is the upstream pause reference rather than whichever
# mirror the cluster it was taken from happened to use.
#
# Two different provenances here, and the difference matters enough to state.
# The row shape and its values are real kubelet output with the identifying
# labels rewritten. The four INTERFACE NAMES are not measured: they are what
# KubeVirt bridge binding produces in a worker Pod netns -- a dummy carrying the
# original name and the Pod IP, the real NIC renamed with a -nic suffix, the
# bridge, and the tap -- read from the generators in the version this repo
# ships. A virt-launcher Pod was not available to sample, so the names are
# derived rather than observed, and the counters attached to them are
# illustrative of direction, not of magnitude.
worker_pod='virt-launcher-kubernetes-test-latest-version-md0-abc12-xyz34'
worker_labels_for() {
  printf 'container="",id="/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod0000000a_0000_0000_0000_00000000000b.slice/cri-containerd-0000000000000000000000000000000000000000000000000000000000000001.scope",image="registry.k8s.io/pause:3.10",interface="%s",name="0000000000000000000000000000000000000000000000000000000000000001",namespace="tenant-test",pod="%s"' "$1" "${worker_pod}"
}
worker_labels="$(worker_labels_for eth0)"
kubectl_raw_output=

# Built rather than pasted so the eight families cannot drift apart by a typo in
# one of them, and so a test that needs one family can name it.
worker_series() {
  local labels="$1"
  local rx_bytes="${2:-4.4040192e+07}"
  local rx_packets="${3:-31284}"
  printf 'container_network_receive_bytes_total{%s} %s 1786657157281\n' "${labels}" "${rx_bytes}"
  printf 'container_network_receive_packets_total{%s} %s 1786657157281\n' "${labels}" "${rx_packets}"
  printf 'container_network_receive_packets_dropped_total{%s} 0 1786657157281\n' "${labels}"
  printf 'container_network_receive_errors_total{%s} 0 1786657157281\n' "${labels}"
  printf 'container_network_transmit_bytes_total{%s} 1.820267529e+09 1786657157281\n' "${labels}"
  printf 'container_network_transmit_packets_total{%s} 932294 1786657157281\n' "${labels}"
  printf 'container_network_transmit_packets_dropped_total{%s} 285 1786657157281\n' "${labels}"
  printf 'container_network_transmit_errors_total{%s} 0 1786657157281\n' "${labels}"
}

# The four interfaces a bridge-bound worker Pod presents. eth0 is the dummy and
# reports zero; eth0-nic is the NIC whose receive answers the question. Both
# have to survive the filter, or the artifact holds a number the reader cannot
# interpret -- or worse, only the zero.
bridge_bound_worker() {
  worker_series "$(worker_labels_for eth0)" 0 0
  worker_series "$(worker_labels_for eth0-nic)" 4.4040192e+07 31284
  worker_series "$(worker_labels_for k6t-eth0)" 4.4040192e+07 31284
  worker_series "$(worker_labels_for tap0)" 1.2e+05 900
}

# The node's own interfaces arrive in the same families with both selector
# labels empty. Kept in the default fixture rather than staged only where it is
# asserted: it is what the wire actually carries, and a filter that lets it
# through would report the sandbox node's uplink under a worker heading.
node_series() {
  printf 'container_network_receive_bytes_total{container="",id="/",image="",interface="eth0",name="",namespace="",pod=""} 1.066300417297e+12 1786657157281\n'
  printf 'container_network_transmit_bytes_total{container="",id="/",image="",interface="cilium_host",name="",namespace="",pod=""} 27870 1786657157281\n'
}

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
    # Output before status, for the reason the sibling suite gives: a read cut
    # off part way has already written what it managed to read, and a stub that
    # returns first makes the branch labelling a partial capture unreachable in
    # tests while it stays the likeliest one in production.
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
  # exists and does not carry the label".
  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
  # Branched on awk's exact status. awk exits 1 for "no line matched" and 2 for
  # "I could not evaluate this"; folded into an `if`, both land in the passing
  # branch and the assertion "this is absent" is satisfied by the matcher giving
  # up. Every negative claim in this file rests on the three-way split.
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
# redirected, and every assertion about those files would be reading the tracer.
# Call through this wrapper so the sinks hold what production writes.
run_capture() {
  local _rc=0
  ( set +x; cozy_capture_tenant_worker_network_counters ) || _rc=$?
  return "${_rc}"
}

@test "the capture does not send a reader after a sibling sample it never takes" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  # This capture is read once. Its sibling under the same shared walk is read
  # twice, and the walk is where a stamp would naturally be written for both --
  # which is how a file could end up carrying, on consecutive lines, this
  # capture's own note that no second reading exists and an instruction to
  # subtract one. Two lines of the same artifact contradicting each other is the
  # reading failure this tree is written against, and it costs a reader the time
  # it takes to go looking for a directory that was never created.
  assert_file_contains 'a second capture of the same stream is what turns them into one' "$out"
  assert_file_lacks_pattern 'sibling' "$out"
  assert_file_lacks_pattern 'other sample directory' "$out"
  # The stamp itself stays: when the read happened is true of every caller, and
  # it is what makes a rate computable at all if a second capture is ever added.
  assert_file_contains 'read attempted from' "$out"
  rm -rf "$tmp"
}

@test "a negative assertion fails when its own matcher cannot evaluate" {
  # The helper under every "must not appear" claim in this file. What rides on
  # it is the collector's contract stated negatively: that an unread kubelet was
  # not written up as a quiet link, that a node interface did not land under a
  # worker heading. A matcher that fails open turns each of those into a
  # sentence nobody checked.
  tmp=$(mktemp -d)
  printf 'anything\n' >"$tmp/subject"

  if assert_file_lacks_pattern '*(' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: an unparseable pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  assert_file_lacks_pattern 'absent-string' "$tmp/subject"
  if assert_file_lacks_pattern 'anything' "$tmp/subject" 2>/dev/null; then
    echo "FAIL: a present pattern was reported as absent" >&2
    rm -rf "$tmp"
    return 1
  fi
  rm -rf "$tmp"
}

@test "the eight families of a worker Pod are captured, packets_dropped included" {
  # The positive control every negative claim below is measured against. It
  # names the eight families one by one rather than counting lines, because a
  # count passes while a family is silently missing from the filter -- and the
  # dropped-packet families are the ones a filter written from memory loses,
  # since the obvious spelling for them is *_drops_total, which does not exist.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(node_series; worker_series "$worker_labels")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  for family in \
    container_network_receive_bytes_total \
    container_network_receive_packets_total \
    container_network_receive_packets_dropped_total \
    container_network_receive_errors_total \
    container_network_transmit_bytes_total \
    container_network_transmit_packets_total \
    container_network_transmit_packets_dropped_total \
    container_network_transmit_errors_total; do
    assert_file_contains "${family}{" "$out"
  done
  # And the value, so a filter that kept the family name while dropping the row
  # it was on cannot pass: 285 is the transmit drop count in the fixture.
  assert_file_contains ' 285 ' "$out"
  rm -rf "$tmp"
}

@test "the filter does not select on the container label, which is always empty here" {
  # The trap this collector is likeliest to acquire on its next edit. The CPU
  # counters beside it carry a real container name, so a filter copied from
  # there and tightened with container="compute" matches nothing at all -- and
  # nothing at all reads as a worker that moved no bytes. Every row of this
  # family carries container="", measured against a live kubelet, so the guard
  # is that the fixture's empty-container rows survive.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'container=""' "$out"
  rm -rf "$tmp"
}

@test "node interfaces and foreign namespaces are dropped, workers are kept" {
  # The node's own uplink arrives in the same eight families with pod="" and
  # namespace="", so a filter on the family name alone reports the sandbox
  # node's traffic under a heading that says worker. A Pod of another namespace
  # and a non-worker Pod of this one are the other two rows that must not pass.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  other_ns=$(printf 'container="",interface="eth0",namespace="cozy-system",pod="virt-launcher-elsewhere"')
  other_pod=$(printf 'container="",interface="eth0",namespace="tenant-test",pod="kubernetes-test-latest-version-kube-apiserver-0"')
  kubectl_raw_output="$(node_series; worker_series "$worker_labels"; worker_series "$other_ns"; worker_series "$other_pod")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains "pod=\"${worker_pod}\"" "$out"
  assert_file_lacks_pattern 'pod=""' "$out"
  assert_file_lacks_pattern 'cozy-system' "$out"
  assert_file_lacks_pattern 'kube-apiserver' "$out"
  rm -rf "$tmp"
}

@test "a kubelet that never answered is reported as unknown, not as a link that moved nothing" {
  # The conflation this collector exists to prevent. Zero bytes captured is the
  # same bytes on disk whether the worker received nothing or the kubelet was
  # never read, and only the note tells them apart. Keyed on the exit status
  # rather than on stderr, because `timeout` kills its child without a word and
  # kubectl dies on SIGTERM the same way, so the dominant failure arrives
  # non-zero with an empty error log.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output=
  kubectl_raw_rc=1
  kubectl_raw_stderr='Error from server (Forbidden): nodes "node-a" is forbidden'

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'is unknown' "$out"
  assert_file_contains 'the kubelet was not read' "$out"
  assert_file_lacks_pattern 'reported no network series' "$out"
  # The trailing note is gated on series having arrived, and only the "it
  # appears" direction was pinned. Ungated it would attach "these counters are
  # cumulative..." to a capture holding no counters, which reads as counters
  # that exist. The CPU sibling pins its own note both ways for this reason.
  assert_file_lacks_pattern 'cumulative' "$out"
  assert_file_contains 'forbidden' \
    "$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.read-error.log"
  rm -rf "$tmp"
}

@test "a kubelet that answered without worker rows says which of the two shapes it saw" {
  # Two empty results with different fixes. A namespace present but carrying no
  # virt-launcher-* Pod is a filter problem -- what an upstream rename of the
  # prefix looks like from inside -- and is fixed by looking at what the prefix
  # is now. A node with none of this namespace on it is a scheduling reading and
  # is fixed by looking at where the workers went. Naming the wrong one costs
  # the reader the finding.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  renamed=$(printf 'container="",interface="eth0",namespace="tenant-test",pod="compute-launcher-md0-abc12-xyz34"')
  kubectl_raw_output="$(worker_series "$renamed")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'none of them from a worker Pod named virt-launcher-*' "$out"
  assert_file_lacks_pattern 'no network series for a tenant-test worker' "$out"

  # And the other shape, on a node carrying nothing of this namespace.
  rm -rf "$tmp/snapshots"
  kubectl_raw_output="$(node_series)"
  run_capture
  assert_file_contains 'reported no network series for a tenant-test worker on this node' "$out"
  assert_file_lacks_pattern 'none of them from a worker Pod' "$out"
  rm -rf "$tmp"
}

@test "a filter that could not read its input is not reported as a kubelet with nothing to say" {
  # grep exits 1 for "read it, no match" and 2 for "could not read" -- a full
  # scratch directory on the runner reaches the second. Folded together they
  # land in the arm asserting the kubelet answered and carried no worker series,
  # which is a claim about the cluster manufactured by a local failure.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"
  # grep 2 is what an unreadable input produces; the stub stands in for it
  # without needing a full disk. Scoped to the calls that read a FILE, which is
  # what the three filter stages do -- the node listing pipes through a grep of
  # its own, and failing that one instead would empty the walk and test nothing.
  grep() {
    local _last
    eval "_last=\${$#}"
    if [ -f "${_last}" ]; then
      return 2
    fi
    command grep "$@"
  }

  run_capture
  unset -f grep

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'could not be read back on this runner' "$out"
  assert_file_lacks_pattern 'reported no network series' "$out"
  rm -rf "$tmp"
}

@test "the counters are labelled as totals since the sandbox started, not as a rate" {
  # The reading this capture is most likely to be misread as. Every family here
  # is cumulative from the Pod's sandbox coming up, so a drop count of 285 says
  # nothing about the minutes the node-join deadline covered -- a link that
  # dropped nothing during the failure and a link that dropped throughout carry
  # the same total if the earlier traffic was the same. Saying so in the file is
  # the whole mitigation: a rate needs a second capture of the same stream, and
  # nothing in the artifact would otherwise say the number is not one.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'cumulative' "$out"
  rm -rf "$tmp"
}

@test "a listing that never answered is not reported as a namespace with no workers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_list_rc=1
  kubectl_list_stderr='Unable to connect to the server'

  rc=0
  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  err="$tmp/snapshots/kubernetes-latest/tenant-network-counters/COLLECTION-FAILED.txt"
  assert_file_contains 'failed to list' "$err"
  assert_file_contains 'Unable to connect to the server' "$err"
  rm -rf "$tmp"
}

@test "a namespace with no scheduled worker says so instead of writing an empty tree" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names=

  rc=0
  run_capture || rc=$?

  [ "$rc" -ne 0 ]
  assert_file_contains 'no kubelet to ask' \
    "$tmp/snapshots/kubernetes-latest/tenant-network-counters/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "the node walk stops at its cap and says how many carried a worker" {
  # A cap that drops nodes silently answers for part of the cluster while
  # reading as though it answered for all of it.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a node-b node-c node-d"
  kubectl_raw_output="$(worker_series "$worker_labels")"

  run_capture

  dir="$tmp/snapshots/kubernetes-latest/tenant-network-counters"
  assert_file_contains '4 carried a worker in total' "$dir/COLLECTION-TRUNCATED.txt"
  [ ! -f "$dir/node-d.txt" ]
  rm -rf "$tmp"
}

@test "the read is bounded and the timeout-absent fallback still reads" {
  # Bounding with a binary that is not there turns every read into an exit 127
  # and every note into "the kubelet refused" -- a missing local dependency
  # reported as the cluster failing. The sibling collectors guard the call with
  # command -v for that reason and this one is held to the same shape.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"

  run_capture

  # Bounded: the wrapper saw the raw read, in the -k form the stub demands.
  grep -q 'metrics/cadvisor' "$timeout_calls"
  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'container_network_receive_bytes_total{' "$out"

  # And without `timeout` on PATH the read still happens rather than exiting 127.
  rm -rf "$tmp/snapshots"
  command() {
    if [ "${1:-}" = -v ] && [ "${2:-}" = timeout ]; then
      return 1
    fi
    builtin command "$@"
  }
  run_capture
  unset -f command
  assert_file_contains 'container_network_receive_bytes_total{' "$out"
  assert_file_lacks_pattern 'is unknown' "$out"
  rm -rf "$tmp"
}

@test "a read cut short keeps what arrived and marks it incomplete" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_series "$worker_labels")"
  timeout_fail_node=node-a

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'these counters are incomplete' "$out"
  assert_file_contains 'container_network_receive_bytes_total{' "$out"
  assert_file_contains '[exit 124:' "$out"
  rm -rf "$tmp"
}

@test "a node read whose status never arrived is not written up as a kubelet that answered" {
  # The default behind `rc=${rc:-137}`, and the only route to it. The status
  # comes back through a command substitution now, so a subshell killed outright
  # returns nothing at all -- and the value chosen for that case decides which
  # sentence the artifact carries. Zero is the one value meaning "the kubelet
  # answered", so defaulting there would take the single conclusion this
  # collector may never manufacture and manufacture it from a local failure.
  #
  # Written because the default had none: flipping it back to `:-0` left every
  # suite in this tree green while a killed read was reported as a quiet link.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  # Writes the sinks and returns nothing on stdout: what a killed substitution
  # leaves behind. Stubbed at the helper because no mock of kubectl can produce
  # an empty command substitution while the function still runs.
  _cozy_cadvisor_node_stream() { : >"$2"; : >"$3"; }

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  assert_file_contains 'the kubelet was not read' "$out"
  assert_file_contains '[exit 137:' "$out"
  assert_file_lacks_pattern 'reported no network series' "$out"
  # And the trailing note stays away from a capture that holds no counters: it
  # describes numbers that are not there, and beside an unknown it reads as
  # counters that exist.
  assert_file_lacks_pattern 'cumulative' "$out"
  rm -rf "$tmp"
}

@test "every interface of a bridge-bound worker survives the filter, dummy and NIC alike" {
  # The reading this capture is most easily inverted by. A bridge-bound worker
  # presents four interfaces in its Pod netns, and the one named eth0 is a dummy
  # that holds the Pod IP and carries no traffic -- so it reports zero, and it is
  # the row a reader reaches for first because of its name. The number that
  # answers is the renamed NIC's receive.
  #
  # The filter therefore carries no interface predicate, and this pins that:
  # keeping only one interface would either drop the answer or leave the reader
  # holding the zero. Which row means what is stated where the collector is
  # gated; what this test guarantees is that both are in the artifact to compare.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(node_series; bridge_bound_worker)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-network-counters/node-a.txt"
  for iface in eth0 eth0-nic k6t-eth0 tap0; do
    assert_file_contains "interface=\"${iface}\"" "$out"
  done
  # The dummy really is staged at zero and the NIC is not, so "both survive" is
  # a statement about two distinguishable rows rather than two copies.
  grep -q "^container_network_receive_bytes_total{.*interface=\"eth0\".*} 0 " "$out"
  grep -q "^container_network_receive_bytes_total{.*interface=\"eth0-nic\".*} 4.4040192e+07 " "$out"
  # And the node's own interfaces still do not reach a worker heading.
  assert_file_lacks_pattern 'pod=""' "$out"
  rm -rf "$tmp"
}
