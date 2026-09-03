#!/usr/bin/env bats
# Regression coverage for the tenant-worker block IO capture in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# What this capture answers, and why neither the guest nor the other collectors
# can. A worker that burns its whole vCPU while making no progress is either
# computing and getting nowhere or waiting on blocks, and from outside the Pod
# the two look identical: a busy vCPU thread with a clean cgroup quota. The
# guest's own /proc/diskstats would settle it, and the diagnostics credential
# minted for the guest is os:reader, which Talos does not allow to read file
# contents at all. The kubelet's view of the Pod's cgroup is what remains, and it
# is outside the guest entirely.
#
# The families are the subject of two tests here, because what arrives depends on
# the cgroup version and the artifact has to stay readable either way. Under
# cgroup v2 the kernel's io.stat carries transferred bytes and completed
# operations and nothing else, so the queue-depth and service-time families
# cAdvisor declares for this group are either absent or filled from the node
# filesystem's own counters, which are not this Pod's. A zero read as an idle
# disk is the wrong conclusion this suite pins against.

kubectl_calls=/dev/null
kubectl_node_names=
kubectl_list_rc=0
kubectl_list_stderr=
kubectl_raw_rc=0
kubectl_raw_stderr=

# Row shapes and label sets are upstream cAdvisor's own golden exposition
# (lib/metrics/testdata/prometheus_metrics) with the identifying labels rewritten
# to a worker Pod's; the values are illustrative of direction rather than
# measured. A virt-launcher Pod was not available to sample. Two properties are
# load-bearing and both come from that golden file: the blkio family carries its
# own operation label with Read as one value, and the device label on every one
# of these families is a HOST device rather than anything the guest names.
worker_pod='virt-launcher-kubernetes-test-latest-version-md0-abc12-xyz34'
worker_labels='container="compute",device="/dev/drbd1000",id="/kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod0000000a_0000_0000_0000_00000000000b.slice/cri-containerd-0000000000000000000000000000000000000000000000000000000000000001.scope",image="quay.io/kubevirt/virt-launcher:v1.8.4",name="0000000000000000000000000000000000000000000000000000000000000001",namespace="tenant-test",pod="'"${worker_pod}"'"'
kubectl_raw_output=

# The divisor on its own, because it is also a shape the wire produces on its
# own: cAdvisor publishes it for any container it knows, so a worker whose disk
# counters never arrived still puts this row in the file.
worker_start_time_row() {
  printf 'container_start_time_seconds{%s} 1.786656e+09 1786657157281\n' "${worker_labels}"
}

# The two shapes a cgroup v2 node puts on the wire for one worker: the byte and
# operation counters, plus the blkio family split by operation. The start-time row
# rides along because it is the divisor the counters need, and cAdvisor publishes
# it for any container it knows rather than as part of the disk group. Built rather
# than pasted so a family cannot drift by a typo in one copy.
worker_v2_series() {
  local read_bytes=8.8080384e+07
  worker_start_time_row
  printf 'container_fs_reads_bytes_total{%s} %s 1786657157281\n' "${worker_labels}" "${read_bytes}"
  printf 'container_fs_reads_total{%s} 2148 1786657157281\n' "${worker_labels}"
  printf 'container_fs_writes_bytes_total{%s} 1.048576e+06 1786657157281\n' "${worker_labels}"
  printf 'container_fs_writes_total{%s} 64 1786657157281\n' "${worker_labels}"
  printf 'container_blkio_device_usage_total{container="compute",device="/dev/drbd1000",major="147",minor="1000",namespace="tenant-test",operation="Read",pod="%s"} %s 1786657157281\n' "${worker_pod}" "${read_bytes}"
  printf 'container_blkio_device_usage_total{container="compute",device="/dev/drbd1000",major="147",minor="1000",namespace="tenant-test",operation="Write",pod="%s"} 1.048576e+06 1786657157281\n' "${worker_pod}"
}

# The service-time and queue-depth families as a cgroup v1 node would carry them.
# Kept as a fixture of its own because their absence is what the note in the
# artifact explains, and a suite that only ever stages them present would leave
# that explanation unexercised.
worker_v1_extra_series() {
  printf 'container_fs_read_seconds_total{%s} 33.9 1786657157281\n' "${worker_labels}"
  printf 'container_fs_write_seconds_total{%s} 4.1 1786657157281\n' "${worker_labels}"
  printf 'container_fs_io_current{%s} 3 1786657157281\n' "${worker_labels}"
  printf 'container_fs_io_time_seconds_total{%s} 41.2 1786657157281\n' "${worker_labels}"
  printf 'container_fs_io_time_weighted_seconds_total{%s} 118.7 1786657157281\n' "${worker_labels}"
}

# The IO pressure families as the kubelet publishes them when its pressure gate
# is on. PSI is accounted per cgroup rather than per device, so these rows carry
# the worker's identifying labels with no device label; derived from the shared
# label set rather than pasted so the identity cannot drift between fixtures.
worker_psi_series() {
  local psi_labels
  psi_labels=$(printf '%s' "${worker_labels}" | sed 's|device="[^"]*",||')
  printf 'container_pressure_io_stalled_seconds_total{%s} 12.4 1786657157281\n' "${psi_labels}"
  printf 'container_pressure_io_waiting_seconds_total{%s} 15.9 1786657157281\n' "${psi_labels}"
}

# Two rows that must not reach a file headed "worker": the node's own filesystem,
# which arrives with both selector labels empty, and the Kamaji apiserver, which
# shares the namespace and has a disk of its own.
non_worker_series() {
  printf 'container_fs_reads_bytes_total{container="",device="/dev/sda1",id="/",image="",name="",namespace="",pod=""} 4.294967296e+09 1786657157281\n'
  printf 'container_fs_reads_bytes_total{container="kube-apiserver",device="/dev/sda1",id="/kubepods.slice/x.scope",image="registry.k8s.io/kube-apiserver:v1.33.0",name="x",namespace="tenant-test",pod="kubernetes-test-latest-version-7d9f8b6c5d-abcde"} 5.24288e+08 1786657157281\n'
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
    # Output before status: a read cut off part way has already written what it
    # managed to read, and a stub that returns first makes the branch labelling a
    # partial capture unreachable in tests while it stays the likeliest one in
    # production.
    [ -z "${kubectl_raw_output}" ] || printf '%s\n' "${kubectl_raw_output}"
    return "${kubectl_raw_rc}"
  fi

  return 0
}

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # Return 97 rather than running the command when the wrapper is not the bounded
  # form: a read that lost its ceiling must not pass as a read.
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
  ( set +x; cozy_capture_tenant_worker_block_io ) || _rc=$?
  return "${_rc}"
}

@test "a negative assertion fails when its own matcher cannot evaluate" {
  # The helper under every "must not appear" claim here. What rides on it is this
  # collector's contract stated negatively: that an unread kubelet was not
  # written up as an idle disk, and that the node's own filesystem did not land
  # under a worker heading. A matcher that fails open turns each of those into a
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

@test "the byte, operation and blkio families of a worker Pod are captured" {
  # The positive control every negative claim below is measured against. Families
  # are named one by one rather than counted, because a count passes while one is
  # silently missing from the filter -- and the blkio family is the one a filter
  # written from the fs_* names alone loses, while it is the only one carrying the
  # device major that identifies which volume the reads went to.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'container_fs_reads_bytes_total{' "$out"
  assert_file_contains 'container_fs_reads_total{' "$out"
  assert_file_contains 'container_fs_writes_bytes_total{' "$out"
  assert_file_contains 'container_fs_writes_total{' "$out"
  assert_file_contains 'container_blkio_device_usage_total{' "$out"
  assert_file_contains 'operation="Read"' "$out"
  # The divisor, kept for a different reason from the rest and therefore the one
  # a later edit to the filter is most likely to drop: without it the totals in
  # this file cannot be read as an average at all.
  assert_file_contains 'container_start_time_seconds{' "$out"
  rm -rf "$tmp"
}

@test "one reading is called a total, with the row that turns it into an average" {
  # The claim the file may not make is that a single cumulative reading IS a rate,
  # and the claim it may only make when the row is present is how to divide. Both
  # directions are pinned here, because the note is the only thing standing between
  # a total and an invented throughput figure.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'totals and not a rate' "$out"
  assert_file_contains 'container_start_time_seconds above is the divisor' "$out"
  rm -rf "$tmp"
}

@test "a capture with no start time row does not tell the reader to divide by it" {
  # The same note with its divisor gone. A kubelet can answer with the disk
  # families and no start time -- a filter narrowed later is the likeliest way --
  # and an instruction to subtract a row that is not in the file sends the reader
  # after something that does not exist, which is the failure the note exists to
  # prevent rather than to cause.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series | grep -v '^container_start_time_seconds')"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'The divisor is missing from this file' "$out"
  assert_file_lacks_pattern 'above is the divisor' "$out"
  rm -rf "$tmp"
}

@test "a truncated capture gets no averaging instruction and no missing-family reading" {
  # Rows arrived and the read was then cut off, which is the shape a wedged
  # kubelet produces: the file holds a prefix. Dividing a prefix by the
  # container's age understates it, and a family absent from a prefix says nothing
  # about the kernel interface, so both readings are withheld -- the same rule the
  # CPU note follows for its pairing sentence and the sandbox capture for its
  # column legend.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"
  timeout_fail_node=node-a

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'may be a prefix of what the kubelet had' "$out"
  assert_file_lacks_pattern 'above is the divisor' "$out"
  assert_file_lacks_pattern 'io.stat' "$out"
  assert_file_lacks_pattern 'is a setting rather than a quiet disk' "$out"
  # The device legend is the exception and stays: it explains a label on the rows
  # that did arrive, which is as true of a prefix as of a whole file, and a short
  # file is where a reader needs it most.
  assert_file_contains 'block major 147 for DRBD' "$out"
  timeout_fail_node=
  rm -rf "$tmp"
}

@test "a counter check that could not run claims nothing about the disk" {
  # grep exits 1 for "read it, no match" and 2 for "could not read it", and every
  # sentence past that check is a statement about what the kubelet reported, which
  # a check that never answered cannot support. Driven by handing the hook a path
  # that does not exist, which is the cheapest way to reach a status the walk does
  # not produce on purpose.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)

  ( set +x; _cozy_block_io_tail_note "$tmp/absent.txt" 0 0 1 )

  assert_file_contains 'could not be checked' "$tmp/absent.txt"
  # The device legend still follows: it explains a label on rows, not the file
  # as a whole, and the arm's own sentence sends the reader to the rows above.
  assert_file_contains 'block major 147 for DRBD' "$tmp/absent.txt"
  assert_file_lacks_pattern 'above is the divisor' "$tmp/absent.txt"
  assert_file_lacks_pattern 'divisor is missing' "$tmp/absent.txt"
  assert_file_lacks_pattern 'no block IO counter reached' "$tmp/absent.txt"
  rm -rf "$tmp"
}

@test "the IO pressure families survive the filter where the kubelet publishes them" {
  # Asserted on rows rather than on the note text: the note names these families
  # on every capture, so only a brace after the family name proves a row of the
  # family itself came through the filter.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series; worker_psi_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'container_pressure_io_stalled_seconds_total{' "$out"
  assert_file_contains 'container_pressure_io_waiting_seconds_total{' "$out"
  rm -rf "$tmp"
}

@test "the queue depth and service time families are captured where they exist" {
  # The cgroup v1 shape. The filter has to keep them, or a node that does publish
  # them produces an artifact identical to one that cannot -- and the difference
  # is the whole reason the note beside the rows explains which families the
  # cgroup version costs.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series; worker_v1_extra_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'container_fs_read_seconds_total{' "$out"
  assert_file_contains 'container_fs_write_seconds_total{' "$out"
  assert_file_contains 'container_fs_io_current{' "$out"
  assert_file_contains 'container_fs_io_time_seconds_total{' "$out"
  assert_file_contains 'container_fs_io_time_weighted_seconds_total{' "$out"
  rm -rf "$tmp"
}

@test "a capture with no service time family says what the cgroup version costs" {
  # The reading this collector is most likely to be misread on, and the one a row
  # cannot state for itself: under cgroup v2 io.stat carries bytes and operations
  # only, so a missing service time is the kernel interface rather than a disk
  # that was never touched. Asserted on the cgroup v2 fixture, which is the shape
  # the sandbox actually produces.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'io.stat' "$out"
  # And the row that carries the attribution, because a reader looking for the
  # guest's system disk has several devices to choose between and the major is
  # what decides.
  assert_file_contains 'block major 147 for DRBD' "$out"
  # The families that would answer the question outright if the kubelet published
  # them. Their absence is a setting rather than a quiet disk, and a reader with
  # no sentence for that reads the silence as the answer.
  assert_file_contains 'container_pressure_io_stalled_seconds_total' "$out"
  rm -rf "$tmp"
}

@test "the capture does not send a reader after a sibling sample it never takes" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  # This capture is read once, and it shares its walk with a capture that is read
  # twice. A file telling the reader to subtract a sibling that was never written
  # costs them the time it takes to go looking for a directory that does not
  # exist, so the instruction here has to be the opposite one.
  assert_file_contains 'a second capture of the same stream' "$out"
  assert_file_lacks_pattern 'other sample directory' "$out"
  assert_file_contains 'read attempted from' "$out"
  rm -rf "$tmp"
}

@test "the node filesystem and the tenant control plane stay out of a worker heading" {
  # Both rows are what the wire actually carries. The node's own filesystem
  # arrives in the same families with both selector labels empty, and the Kamaji
  # apiserver shares the namespace with the workers and has a disk of its own --
  # under a heading that says worker, either one answers the question with the
  # wrong subject.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(non_worker_series; worker_v2_series)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains "pod=\"${worker_pod}\"" "$out"
  assert_file_lacks_pattern 'kube-apiserver' "$out"
  assert_file_lacks_pattern 'pod=""' "$out"
  rm -rf "$tmp"
}

@test "a start time with no counter beside it is not written up as an idle disk" {
  # The shape the divisor row makes possible, and the reason it is not free: the
  # walk decides "the kubelet reported nothing for this subject" from whether any
  # row survived, and this family survives for any container cAdvisor knows. So a
  # worker whose disk counters never arrived leaves a file that is not empty, the
  # walk's own "no series" sentence cannot fire, and without the note below the
  # artifact would carry a start time, an instruction for dividing counters, and
  # no counters.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_start_time_row)"

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'no block IO counter reached this file' "$out"
  assert_file_lacks_pattern 'above is the divisor' "$out"
  assert_file_lacks_pattern 'io.stat' "$out"
  rm -rf "$tmp"
}

@test "a kubelet that knows nothing about the container is not written up as an idle disk" {
  # The other empty direction, and the one the walk does answer: cAdvisor
  # contributes none of these families for a container it does not know, so the
  # file is empty and the walk says which of its own arms fired. An empty file
  # reads as a worker that touched no disk, which is the conclusion the whole
  # capture was added to test.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output='container_cpu_usage_seconds_total{container="compute",namespace="tenant-test",pod="'"${worker_pod}"'"} 412.5 1786657157281'

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  # The walk builds this sentence from the subject word the capture passes, and
  # that word names the divisor family too, because the file can hold the divisor
  # with no counter beside it and a sentence naming only block IO would be false
  # about such a file.
  assert_file_contains 'no block IO or container start-time series' "$out"
  rm -rf "$tmp"
}

@test "a kubelet that never answered is not written up as an idle disk either" {
  # The other half of the same claim, and the one the status decides rather than
  # the output: timeout kills its child without a word, so this arrives non-zero
  # with an empty error log, and a collector keyed on stderr would land in the arm
  # that says the kubelet answered.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output=
  timeout_fail_node=node-a

  run_capture

  out="$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  assert_file_contains 'is unknown' "$out"
  assert_file_contains '[capture exit code: 124]' "$out"
  assert_file_lacks_pattern 'io.stat' "$out"
  timeout_fail_node=
  rm -rf "$tmp"
}

@test "every read this capture makes carries a wall clock bound" {
  # The property that keeps this collector from costing the artifacts behind it.
  # It runs on the failure path, where the apiserver and the kubelet are the two
  # components least likely to answer, and an unbounded read here holds the
  # chainsaw op until the op is killed -- which loses the tenant snapshot rather
  # than truncating it. The mock refuses any call that is not the bounded form, so
  # a read that lost its ceiling exits 97 and the file says so.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  export COZY_REPORT_DIR="$tmp"
  COZY_SNAPSHOT_NAME=kubernetes-latest
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_node_names="node-a"
  kubectl_raw_output="$(worker_v2_series)"

  run_capture

  # Two reads: the virt-launcher listing and the node's metric stream. Both go
  # through the wrapper, and the count is asserted so a read added later without
  # one fails here rather than in a timed-out run.
  [ "$(grep -c 'kubectl' "$timeout_calls")" -eq 2 ]
  assert_file_lacks_pattern 'exit code: 97' "$tmp/snapshots/kubernetes-latest/tenant-block-io/node-a.txt"
  rm -rf "$tmp"
}
