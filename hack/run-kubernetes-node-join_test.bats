#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for cozy_report_node_join_failure in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh -- the diagnostics the kubernetes-*
# suites emit when fewer than 2 tenant nodes become Ready inside the 18m
# deadline.
#
# What these pin is not what the block prints but that it finishes. It runs
# exactly when the cluster is misbehaving, and its first read goes through the
# tenant kubeconfig to the tenant apiserver -- the component least likely to
# answer in a node-join failure. So every read carries its own wall-clock bound
# and every walk a cap, because an unbounded read here does not lose only
# itself: it holds the Chainsaw op until the op is killed, and the tenant
# crust-gather snapshot that the caller's exit 1 triggers is then lost rather
# than truncated.
#
# The kubectl stub hangs, refuses and answers in part on purpose. A stub that
# always answers cleanly would leave every test here green against an entirely
# unbounded implementation, which is the state this suite was written against.
#
# The block also calls talos_image_cache_diagnose (hack/e2e-chainsaw/_lib/
# talos-image-cache.sh), whose reads are bounded for the same reason and are
# covered here rather than in that file's own suite, which is mock-free by
# design. Its reachability re-probe is deliberately not bounded there: it spends
# its budget inside a Pod it creates, and shrinking that budget would change
# which image factory the happy path picks.
#
# cozytest.sh's awk parser ends an @test block at the first bare closing brace,
# so command mocks stay at top level, and there is no bats `run`/`$status`:
# assertions are direct shell tests that exit non-zero on failure. EXIT-trap
# cleanup is banned in hack/*.bats and frozen by a guard in hack/cozyreport.bats,
# so each test removes its own temp dir on its last line.
#
# Run with: hack/cozytest.sh hack/run-kubernetes-node-join_test.bats
# -----------------------------------------------------------------------------

kubectl_calls=/dev/null
unbounded_calls=/dev/null
timeout_calls=/dev/null
# Set by the timeout mock around the command it runs, so the kubectl mock can
# record which reads reached it without a bound. This is the whole point of the
# pair: asserting on the timeout log alone proves that SOME reads are bounded,
# never that none escaped.
in_bound=0
kubectl_fail_match=
kubectl_fail_rc=1
timeout_fail_match=
timeout_fail_rc=124
importer_pod_names=
kubectl_gate_output=
# Fixed clock. `date +%s` decides whether the phase still has budget, so the
# tests that are about that decision have to own the clock rather than race it.
date_now=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"
  if [ "${in_bound}" != 1 ]; then
    printf '%s\n' "$*" >>"${unbounded_calls}"
  fi

  if [ -n "${kubectl_fail_match}" ]; then
    case "$*" in
      *"${kubectl_fail_match}"*) return "${kubectl_fail_rc}" ;;
    esac
  fi

  case "$*" in
    *"get pods -l kubevirt.io=virt-launcher"*"spec.nodeName"*)
      # One node, so the throttle capture proceeds past its listing into the
      # per-node kubelet read. Without this the walk ends at an empty node set
      # and the audit only ever sees the listing -- an unbounded `get --raw`
      # added later would slip past the one instrument that watches it.
      #
      # Narrowed to the node-name projection because two other reads in this
      # block carry the same label selector, and a node name is not an answer
      # either of them could have received.
      printf 'srv1\n'
      return 0
      ;;
    *"get nodes"*"InternalIP"*)
      # Name and address, the shape the sandbox CPU time capture asks for. One
      # node, so the walk proceeds past its listing into the per-node Talos
      # read -- the read the audit above is the only instrument for.
      printf 'srv1|192.0.2.11\n'
      return 0
      ;;
    *"get pods -o name"*)
      [ -z "${importer_pod_names}" ] || printf '%s\n' ${importer_pod_names}
      return 0
      ;;
    *"get deploy talos-image-cache"*)
      # Empty stdout with exit 0 is what --ignore-not-found returns for a cache
      # that is not deployed, which is the default here.
      [ -z "${kubectl_gate_output}" ] || printf '%s\n' "${kubectl_gate_output}"
      return 0
      ;;
  esac
  return 0
}

# The sandbox nodes are read over the Talos API rather than through the
# apiserver, so a mock for kubectl alone leaves those reads outside the audit
# above: an unbounded talosctl call would hold the op exactly as an unbounded
# kubectl one does. Recorded through the same in_bound pair for that reason.
talosctl_calls=/dev/null
talosctl() {
  printf '%s\n' "$*" >>"${talosctl_calls}"
  if [ "${in_bound}" != 1 ]; then
    printf '%s\n' "$*" >>"${unbounded_calls}"
  fi
  # A plausible /proc/stat first line. The collector files a note when the read
  # returns nothing, so a mock that printed nothing would drive every test here
  # down that arm instead of the one being exercised.
  printf 'cpu  10 0 5 900 1 0 0 7 4000 0\n'
}

# The sampling interval is a real wait in production and a recorded call here.
# Without the mock every test that drives the block would pay it in wall clock;
# with it, the log is also what pins the interval being taken ONCE for both
# collectors rather than once each.
sleep_calls=/dev/null
sleep() {
  printf '%s\n' "$*" >>"${sleep_calls}"
}

date() {
  if [ -n "${date_now}" ] && [ "${1:-}" = +%s ]; then
    printf '%s\n' "${date_now}"
    return 0
  fi
  command date "$@"
}

timeout() {
  local rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  # The idiom every bounded read in this tree uses is `timeout -k <grace> <n>`.
  # Anything else reaching this mock is a read bonded some other way, and 97 is
  # a status no kubectl produces, so it cannot be mistaken for the read's own.
  [ "${1:-}" = -k ] || return 97
  shift 3

  if [ -n "${timeout_fail_match}" ]; then
    case " $* " in
      # A killed read is modelled as producing nothing: `timeout` SIGKILLs the
      # child, so the command may never write a byte. Returning before running
      # it is what makes the "cut off" note the only trace of that read.
      *"${timeout_fail_match}"*) return "${timeout_fail_rc}" ;;
    esac
  fi

  in_bound=1
  "$@" || rc=$?
  in_bound=0
  return "${rc}"
}

# Point a test's report dir at its own temp dir.
#
# The collectors this file drives write into COZY_REPORT_DIR. Unset, the
# library falls back to /workspace/_out, which is not writable under the bare
# `bats` binary. The collector does not die there -- its call site's `|| true`
# suspends set -e for the whole body, so mkdir fails, the reads then fail on
# their own redirects into the directory that was never created, and any
# assertion about those reads fails for a reason that has nothing to do with
# the code. cozytest.sh hides this by defaulting the variable to a
# repo-relative path, so the two runners disagree, which is the divergence
# docs/agents/e2e-testing.md warns about. Pointing it at the temp dir also
# keeps the suite from writing into _out/ under the snapshot name the real e2e
# uses.
#
# Called from every test that stages a temp dir -- 20 of the 22 here; the two
# that do not are pure source greps over the library and stage nothing. Among
# the 20 are the four whose work happens inside a
# `bash -c` subprocess. Exported so it reaches those too: they stub every
# collector today and write nothing, but a stub that later grows a real read
# would put its report under the tree the e2e run uses, and the call that was
# supposed to prevent that would have been decorative the whole time.
#
# Assigned per test rather than in a bats `setup`: cozytest.sh calls each @test
# body directly and never runs setup, so a setup-based version would work under
# one runner and not the other -- the same divergence one level up.
use_temp_report_dir() {
  export COZY_REPORT_DIR="$1/report"
}

# The sandbox CPU time capture refuses to read anything without a talosconfig,
# and says so rather than leaving an empty directory. Tests that are about the
# reads have to get past that, and tests that are about the refusal have to be
# sure the file is absent -- which is not the same as leaving TALOSCONFIG unset,
# since the fallback path is repo-relative and a developer's tree can carry a
# stale one from a local sandbox run. Both directions are therefore explicit.
stage_sandbox_talosconfig() {
  printf 'context: e2e\n' >"$1/talosconfig"
  export TALOSCONFIG="$1/talosconfig"
}

no_sandbox_talosconfig() {
  export TALOSCONFIG="$1/talosconfig-absent"
}

# The block calls collectors that have their own suites and their own bounds.
# The ones here are stubbed everywhere; the ones under stub_gated_collectors
# below are stubbed only where the test is about the budget rather than the
# reads, for the reason given there. Stubbed after sourcing so these tests are
# about the block's own reads; left un-stubbed they would drag a Certificate, a
# helper Pod and a cache probe into every case here.
stub_collectors() {
  cozy_report_guest_console_wedge() { printf 'wedge-stub\n'; }
  cozy_capture_tenant_serial_console() { printf 'serial-console-stub\n'; }
  cozy_capture_tenant_talos() { printf 'talos-stub\n'; }
  talos_image_cache_diagnose() { printf 'image-cache-stub\n'; }
  # Stubbed here rather than left to the gated set, and the audit below loses
  # nothing by it: that audit works by letting real collector bodies reach the
  # kubectl mock and failing on any read taken outside a `timeout` wrapper, and
  # this one issues no kubectl read at all -- it shells out to `unshare` and
  # `mount`. Un-stubbed it does that for real, so on Linux the unit suite
  # attempts to mount debugfs with whatever privileges the host gives it -- this
  # suite runs from `make unit-tests` on an ordinary VM rather than in the
  # privileged sandbox container, so whether the attempt succeeds is the host's
  # answer and not the code's -- while on macOS there is no `unshare` at all and
  # the collector stops at its own precondition, so the path is never exercised.
  # A unit test whose result is set by the machine rather than by the code is
  # the property being removed, and the `timeout` mock in this file is a
  # pass-through, so a `mount` that hung would hang the suite with no ceiling to
  # stop it.
  cozy_capture_sandbox_kvm_exits() { printf 'kvm-exits-stub\n'; }
  # The two readings beside it, stubbed on the same ground and for a sharper
  # version of it: neither issues a kubectl read, so neither contributes to the
  # audit below, and un-stubbed they do not probe the host -- they READ it. One
  # cats the runner's real /proc/stat and the other walks the whole of /proc,
  # opening a comm file per process on the machine running `make unit-tests`.
  # The result of a case here would then be set by whatever else that machine
  # happens to be running, which is the property this file removes.
  cozy_capture_runner_kernel_cpu_time() { printf 'runner-kernel-cpu-stub\n'; }
  cozy_capture_sandbox_qemu_thread_cpu() { printf 'sandbox-qemu-thread-stub\n'; }
  # Stubbed on the same ground and for a sharper version of it again: the canary
  # issues no kubectl read either, so it contributes nothing to the audit below,
  # and un-stubbed it does not read this machine -- it LOADS it, occupying a
  # core for a couple of seconds on every case that drives the block. What it
  # would then record is set by whatever else is running on the machine, which
  # is the property this file removes.
  cozy_capture_runner_canary() { printf 'runner-canary-stub\n'; }
}

# The collectors below are deliberately NOT in stub_collectors, and that is
# load-bearing rather than an omission. The block-level audit above --
# "no node join diagnostic read escapes a wall clock bound" -- works by letting
# the real collectors run against the kubectl mock and failing on any read that
# reached it outside a `timeout` wrapper. Stubbing a collector there does not
# make that test pass more easily; it removes the collector from the audit
# entirely, and the test stays green because there is nothing left to audit.
# Each of them carries reads the audit is the only instrument that sees:
# ghcr_mirror_diagnose issues five, and every cadvisor capture issues a Pod
# listing and a node read apiece. Their own suites substitute a grep over the
# source, which by construction cannot see a read that was added without a
# bound.
#
# They are stubbed only where the test is about the phase budget rather than
# about the reads, which is the one place their real bodies would drown the
# signal.
stub_gated_collectors() {
  cozy_capture_tenant_worker_cpu_throttle() { printf 'cpu-throttle-stub\n'; }
  cozy_capture_tenant_worker_network_counters() { printf 'network-counters-stub\n'; }
  cozy_capture_tenant_worker_block_io() { printf 'block-io-stub\n'; }
  cozy_capture_sandbox_node_cpu_time() { printf 'sandbox-cpu-time-stub\n'; }
  cozy_capture_tenant_worker_thread_cpu() { printf 'thread-cpu-stub\n'; }
  ghcr_mirror_diagnose() { printf 'ghcr-mirror-stub\n'; }
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

# The sibling suites' three-way split, and here for the same reason. A bare
# `! grep -q` succeeds on a file that does not exist, which is
# indistinguishable from the file existing without the pattern; and awk exits 1
# for "no line matched" and 2 for "I could not evaluate this", so folding those
# together lets a broken matcher satisfy a negative assertion. A positive
# assertion fails loudly when its matcher breaks and gets fixed; this one would
# go green and stay green.
assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"
  local _rc=0

  if [ ! -f "${file}" ]; then
    printf 'expected %s to exist so it could be checked for: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
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

@test "no node join diagnostic read escapes a wall clock bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  stage_sandbox_talosconfig "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  talosctl_calls="$tmp/talosctl.calls"
  sleep_calls="$tmp/sleep.calls"
  : >"$unbounded_calls"
  importer_pod_names='pod/importer-md0-a pod/importer-md0-b'

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # One unbounded read is the whole defect: against a wedged tenant apiserver it
  # holds the op to its 50m ceiling, and everything scheduled after it -- the
  # tenant crust-gather snapshot above all -- is lost rather than truncated.
  if [ -s "$unbounded_calls" ]; then
    echo "FAIL: these reads ran with no wall-clock bound:" >&2
    cat "$unbounded_calls" >&2
    false
  fi
  # And the block did read: without this the check above passes just as well on
  # an implementation that reads nothing at all.
  [ "$(grep -c . "$kubectl_calls")" -ge 11 ]
  # Named collectors, not just a count. The gated collectors below are the ones
  # this audit is the only instrument for, and each is one line away from
  # vanishing from it: moving its stub into stub_collectors takes it out of the
  # run entirely, and every suite stays green because the audit has nothing left
  # to look at. A count cannot notice that -- the other reads keep it satisfied
  # -- so each collector is pinned individually.
  #
  # Pinned by the report directory each capture writes, not by a read only it
  # issues. The cAdvisor captures run one shared body, so their Pod listing
  # and node read are byte-identical and neither has a read of its own: a
  # listing-keyed pin is satisfied by whichever of them ran, and stays green
  # with the other absent from the audit entirely. The directory is the one
  # thing each produces alone.
  if ! grep -q 'get deploy ghcr-mirror' "$kubectl_calls"; then
    echo "FAIL: ghcr_mirror_diagnose did not run, so its reads were not audited" >&2
    false
  fi
  # Keyed on the directory having CONTENT, not on the directory existing. Each
  # capture mkdir -p's its report directory before it lists anything, so an
  # empty one is exactly what a capture that issued no read leaves behind -- and
  # that is the state this pin has to reject, since a capture missing from the
  # audit is a capture whose reads nobody bounded.
  for subdir in tenant-cpu-throttle tenant-network-counters tenant-block-io sandbox-host-cpu-time; do
    dir="$COZY_REPORT_DIR/snapshots/kubernetes/$subdir"
    if [ -z "$(find "$dir" -type f 2>/dev/null | head -n 1)" ]; then
      echo "FAIL: $subdir produced no file, so that capture issued no read and its reads were not audited" >&2
      ls -la "$COZY_REPORT_DIR/snapshots/kubernetes" >&2 || true
      false
    fi
  done
  rm -rf "$tmp"
}

@test "the counters are read twice, one interval apart, and the interval is paid once" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  stage_sandbox_talosconfig "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  talosctl_calls="$tmp/talosctl.calls"
  sleep_calls="$tmp/sleep.calls"
  : >"$sleep_calls"
  COZY_DIAG_RATE_INTERVAL=7

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # A single reading of either subject is an average over an uptime: every
  # counter on both sides is cumulative, so one file says what the container has
  # used since it started and nothing about the window the deadline covered. The
  # pair is the instrument, and a second reading that never happened leaves a
  # directory that looks collected.
  for subject in tenant-cpu-throttle sandbox-host-cpu-time; do
    for sample in 1 2; do
      dir="$COZY_REPORT_DIR/snapshots/kubernetes/$subject/sample-$sample"
      if [ -z "$(find "$dir" -type f 2>/dev/null | head -n 1)" ]; then
        echo "expected $subject to leave a reading under sample-$sample" >&2
        ls -R "$COZY_REPORT_DIR/snapshots/kubernetes" >&2 || true
        return 1
      fi
    done
  done
  # Once, not once per subject. Both subjects answer the same question about the
  # same window, so they are read on either side of one wait; paid per subject
  # the wait would be the larger half of what this pair adds to a failing run,
  # and the two windows would no longer line up.
  waits=$(grep -c . "$sleep_calls" || true)
  if [ "$waits" -ne 1 ]; then
    echo "expected exactly one interval between the two readings; got $waits" >&2
    cat "$sleep_calls" >&2
    return 1
  fi
  assert_file_contains '7' "$sleep_calls"
  rm -rf "$tmp"
}

@test "an interval that would put both readings at the same instant is refused" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  stub_gated_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  sleep_calls="$tmp/sleep.calls"
  : >"$sleep_calls"
  # Zero is all digits and passes every check the other knobs make, and it is
  # the one value that leaves this pair collected and meaningless: two readings
  # at the same instant subtract to nothing. Unlike the read bounds, where zero
  # removes a ceiling, here it removes the measurement while leaving the files.
  COZY_DIAG_RATE_INTERVAL=0

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  assert_file_contains 'ignoring COZY_DIAG_RATE_INTERVAL' "$tmp/out"
  # The reason, not just the refusal. The shared helper's default parenthetical
  # says zero disables a bound, which is true of every other knob it guards and
  # false of this one: zero here removes no ceiling, it puts both readings at
  # the same instant. A warning that names the wrong reason sends the reader to
  # look for a ceiling that was never involved.
  assert_file_contains 'both readings at the same instant' "$tmp/out"
  assert_file_lacks_pattern 'COZY_DIAG_RATE_INTERVAL.*disables the bound' "$tmp/out"
  # The default, not the rejected value: a warning that named the fallback and
  # then used the value anyway would be worse than no warning.
  assert_file_contains "$COZY_DIAG_RATE_INTERVAL_DEFAULT" "$sleep_calls"
  rm -rf "$tmp"
}

@test "a sandbox with no talosconfig is recorded rather than left as an empty directory" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  no_sandbox_talosconfig "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  sleep_calls="$tmp/sleep.calls"

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # Steal is the quantity that separates a sandbox node oversubscribed from
  # inside from one not given its turn by the machine under it. An empty
  # directory here reads as the first, which is a conclusion nothing observed.
  assert_file_contains 'no sandbox talosconfig at' \
    "$COZY_REPORT_DIR/snapshots/kubernetes/sandbox-host-cpu-time/sample-1/COLLECTION-FAILED.txt"
  rm -rf "$tmp"
}

@test "a read cut off before it answered says so and the reads after it still run" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  # The first read of the block, through the tenant kubeconfig -- the read the
  # issue names as the one that hangs on a wedged tenant apiserver.
  timeout_fail_match='describe nodes'
  timeout_fail_rc=124

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  assert_file_contains 'was cut off' "$tmp/out"
  # A cut-off read is silence about the cluster, not a finding about it. Reported
  # any other way, the next reader takes an empty node table for an empty cluster.
  assert_file_contains 'absent from this log, not absent from the cluster' "$tmp/out"
  # The reads after it are the point of bounding it. The CSR list is what
  # separates a worker that booted and never registered from one that never
  # booted, and it comes last in the block.
  assert_file_contains 'get csr' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "a read killed by the grace signal is named as a kill rather than as a deadline" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_fail_match='describe nodes'
  timeout_fail_rc=137

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # 137 is 128+SIGKILL, which the -k grace produces and so does an OOM kill or a
  # teardown signalling the group. Quoting the deadline there would state a cause
  # that was never observed, and a read killed at second two is not one that ran
  # the full twenty. Its two siblings are pinned; without this the wording can rot.
  assert_file_contains 'was killed before it finished (SIGKILL)' "$tmp/out"
  if grep -q "did not finish within" "$tmp/out"; then
    echo "FAIL: a SIGKILL was reported as the deadline elapsing" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "lowering the diagnostic read budget after sourcing moves both of its bounds" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  # The same property the cache-side knob is pinned for, on the caller's own knob:
  # both the wall clock and the --request-timeout pasted beside it have to follow a
  # value lowered after sourcing, which is the only moment a test can lower it.
  COZY_DIAG_READ_TIMEOUT=3

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  assert_file_contains '-k 5 3' "$timeout_calls"
  assert_file_contains '--request-timeout=3s' "$kubectl_calls"
  if grep -q -- '-k 5 20' "$timeout_calls"; then
    echo "FAIL: the wall-clock bound stayed at 20s after the knob was lowered:" >&2
    grep -- '-k 5 20' "$timeout_calls" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a read that failed is named with its status instead of passing in silence" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_fail_match='get hr'
  kubectl_fail_rc=1

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # `|| true` on a read is how a refused call becomes indistinguishable from a
  # resource that is not there. The status is what tells them apart.
  assert_file_contains 'read failed (exit 1)' "$tmp/out"
  rm -rf "$tmp"
}

@test "the importer log walk stops at its cap and records both counts" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  importer_pod_names='pod/importer-a pod/importer-b pod/importer-c pod/importer-d pod/importer-e'

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # Two reads per Pod (current and previous), so the cap is what keeps this loop
  # from being an unbounded term in a block whose whole purpose is to finish.
  [ "$(grep -c 'logs pod/importer' "$kubectl_calls")" -eq 6 ]
  # A short listing otherwise reads as a small set of importers rather than a
  # truncated walk, which is the reading that sends the next person looking for
  # an importer that was never printed.
  assert_file_contains 'stopped after 3' "$tmp/out"
  assert_file_contains '5 matched' "$tmp/out"
  rm -rf "$tmp"
}

@test "an importer listing that failed is not reported as no importers" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_fail_match='get pods -o name'
  kubectl_fail_rc=1
  importer_pod_names='pod/importer-a'

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # The DataVolume import stage is the sub-mode where the OS image never
  # finishes importing. "No importer Pods" is a real finding there; a listing
  # that never answered is not that finding, and must not be filed as it.
  assert_file_contains 'unknown, not none' "$tmp/out"
  if grep -q 'logs pod/importer' "$kubectl_calls"; then
    echo "FAIL: walked an importer Pod from a listing that failed" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a cache gate read that was cut off is not reported as a cache that was never deployed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_fail_match='get deploy talos-image-cache'
  timeout_fail_rc=124

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  # "not deployed" is a claim about the cluster. Drawn from a read that never
  # answered, it retires the whole cache hypothesis on this failure path -- the
  # one the section exists to test -- on the strength of a call that failed.
  assert_file_contains 'unknown, not no' "$tmp/out"
  if grep -q 'not deployed' "$tmp/out"; then
    echo "FAIL: a cut-off gate read was announced as a cache that is not deployed" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a cache gate read that failed is not reported as a cache that was never deployed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_fail_match='get deploy talos-image-cache'
  kubectl_fail_rc=1

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  # Exit 1 is what `kubectl get` returns for a refused connection, for
  # Unauthorized and for an unrecognised kind -- not only for NotFound. Reading
  # it as "absent" retires the cache hypothesis on the one path that tests it.
  assert_file_contains 'unknown, not no' "$tmp/out"
  if grep -q 'not deployed' "$tmp/out"; then
    echo "FAIL: a failed gate read was announced as a cache that is not deployed" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a cache gate that answered with nothing reports the cache as not deployed" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  # --ignore-not-found makes absent an exit 0 with empty output, and that is a
  # real finding: the run used the public factory, so there is nothing to probe.
  # Without this the fix above would just report everything as unknown.
  assert_file_contains 'not deployed' "$tmp/out"
  assert_file_contains 'ignore-not-found' "$timeout_calls"
  rm -rf "$tmp"
}

@test "the diagnostics phase declines the rest out loud once its budget is spent" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  # This test is about which collectors the budget declines, so the two that
  # stay real elsewhere are stubbed here: their reads would say nothing about
  # the decline and their markers are what the loop below checks for absence.
  stub_gated_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  : >"$kubectl_calls"
  # The clock is already past the deadline when the phase opens, so every
  # collector after cozy_diag_phase_start is declined.
  date_now=1000000000
  COZY_DIAG_PHASE_BUDGET=0

  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1

  # The snapshot is the artifact this budget exists to protect, and the note has
  # to say so: a reader who finds a short bundle otherwise cannot tell a phase
  # that ran out from a cluster with nothing to report.
  assert_file_contains 'not collected' "$tmp/out"
  assert_file_contains 'needs the rest of the op' "$tmp/out"
  assert_file_contains 'nothing here was observed either way' "$tmp/out"
  # Named per read, and named for the two whose stdout is piped through grep
  # above all: a note on stdout there is dropped by the filter, so asserting only
  # that SOME collector announced its decline passes while those two go silent.
  assert_file_contains 'worker DataVolume/PVC phases: not collected' "$tmp/out"
  assert_file_contains 'worker DataVolume detail: not collected' "$tmp/out"
  # A declined listing never ran, so it must not be reported as one that answered.
  # This is the third state -- answered-and-empty, failed, never-issued -- and the
  # conclusion it would otherwise print is the one the whole (a2) section exists to
  # test: no importer Pod, therefore no import.
  if grep -q 'the listing answered and matched none' "$tmp/out"; then
    echo "FAIL: a declined listing was reported as a namespace with no importer:" >&2
    grep -n 'listing' "$tmp/out" >&2
    false
  fi
  if [ -s "$kubectl_calls" ]; then
    echo "FAIL: reads were issued after the phase ran out of budget:" >&2
    cat "$kubectl_calls" >&2
    false
  fi
  # The gates whose collectors are stubbed here are the mechanism, not a
  # detail: declining the heaviest guest-Talos capture is what the budget
  # derivation is for, and a stub issues no kubectl, so the check above cannot
  # see it run. That is the whole reason the walk is over the stubbed ones and
  # not over every gate in the block -- the importer listing is gated too, and
  # it reads for real, so the check above already covers it. Each stub prints a
  # marker; none of them may appear, and each must have said why. The list is
  # load-bearing rather than descriptive -- a stubbed gate added without a
  # line here is a collector that may run past the deadline while this test
  # stays green, which is exactly what the empty-kubectl_calls check above
  # cannot catch for a collector that issues no reads of its own.
  #
  # The same risk runs the other way, and the other way is the one that already
  # happened: a collector STUBBED in the shared helper stops being visible to
  # the bound audit at the top of this file, which needs it to run for real.
  # A rule written for additions does not catch a removal, so both directions
  # are named here.
  for marker in serial-console-stub talos-stub image-cache-stub cpu-throttle-stub \
    network-counters-stub block-io-stub sandbox-cpu-time-stub thread-cpu-stub \
    ghcr-mirror-stub; do
    if grep -q "$marker" "$tmp/out"; then
      echo "FAIL: $marker ran after the phase ran out of budget" >&2
      false
    fi
  done
  assert_file_contains '(b1) tenant worker guest serial console: not collected' "$tmp/out"
  assert_file_contains '(b) in-guest Talos dmesg + kubelet logs + service states + links: not collected' "$tmp/out"
  assert_file_contains 're-probe talos-image-cache ClusterIP + cacher debug bundle: not collected' "$tmp/out"
  assert_file_contains '(d) tenant worker CPU counters, sandbox node CPU time and worker per-thread CPU time: not collected' "$tmp/out"
  assert_file_contains '(d2) tenant worker network counters: not collected' "$tmp/out"
  assert_file_contains '(d3) tenant worker block IO counters: not collected' "$tmp/out"
  assert_file_contains 'ghcr-mirror state, access log and warm-up Job: not collected' "$tmp/out"
  rm -rf "$tmp"
}

@test "a read outside any diagnostics phase is not gated by the phase budget" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  : >"$kubectl_calls"
  date_now=1000000000
  COZY_DIAG_PHASE_BUDGET=0

  # The scheduling-gate branch reads twice and opens no phase. If an unopened
  # phase counted as expired, that branch would go silent and the node table it
  # prints -- the one thing naming the taint that held the node -- would vanish.
  ( set +x; cozy_diag_read 'tenant node table' kubectl get nodes ) >"$tmp/out" 2>&1

  assert_file_contains 'get nodes' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the cache diagnostic dumps each run inside a wall clock bound" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  # Created up front: the mock only appends, so this file exists today solely because
  # the re-probe's seven calls are unbounded. Once cozystack/cozystack#3666 bounds
  # them the walk below would read a file that was never written and die under set -e
  # -- a test that fails when the thing it tolerates gets fixed.
  : >"$unbounded_calls"
  # The gate has to answer "deployed" or the dumps below it never run.
  kubectl_gate_output='deployment.apps/talos-image-cache'

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  # These three run after the re-probe, on the same path to the same exit, so a
  # hang in any of them costs the snapshot exactly as one in the block above.
  assert_file_contains 'get deploy,pod,svc,endpointslice' "$timeout_calls"
  assert_file_contains 'ciliumclusterwidenetworkpolicy' "$timeout_calls"
  assert_file_contains '-c serve --tail=50' "$timeout_calls"
  # And nothing here escaped a bound. Asserting the three positively proves some
  # reads are bounded, never that none escaped -- the distinction this suite's mock
  # pair exists for, and the one test that checks unbounded_calls stubs this
  # collector out entirely. The re-probe's seven calls are the documented residual,
  # so they are named rather than tolerated by an empty-file check that would also
  # pass on an unbounded dump.
  while read -r call; do
    [ -n "$call" ] || continue
    case "$call" in
      # _talos_image_cache_reachable_from_tenant, unbounded on purpose (see the
      # comment in talos-image-cache.sh) and tracked in its own issue.
      *"get pod -l app.kubernetes.io/name=talos-image-cache"*) continue ;;
      *"exec "*"-c serve"*) continue ;;
      *"get deploy talos-image-cache -o jsonpath"*) continue ;;
      *"delete pod talos-image-cache-probe"*) continue ;;
      *"run talos-image-cache-probe"*) continue ;;
      *"logs talos-image-cache-probe"*) continue ;;
    esac
    echo "FAIL: an unexpected unbounded read in the cache diagnosis: $call" >&2
    false
  done <"$unbounded_calls"
  rm -rf "$tmp"
}

@test "a cache dump cut off mid flight says so instead of leaving a bare section header" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_gate_output='deployment.apps/talos-image-cache'
  timeout_fail_match='get deploy,pod,svc,endpointslice'
  timeout_fail_rc=124

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  # Bounding a dump without a note trades a hang for a lie: `timeout` prints
  # nothing when it fires, so the section header would be followed by nothing, and
  # an empty deploy/pod/svc/endpointslice listing reads as a cache that has no Pod,
  # no Service and no EndpointSlice -- a finding a triager acts on, from a read
  # that never reached the apiserver.
  assert_file_contains 'cache deploy/pod/svc/endpointslice: read did not finish' "$tmp/out"
  assert_file_contains 'absent from this log, not absent from the cluster' "$tmp/out"
  # And the dumps after it still ran, so one cut-off read does not end the section.
  assert_file_contains 'ciliumclusterwidenetworkpolicy' "$timeout_calls"
  rm -rf "$tmp"
}

@test "lowering the cache read budget after sourcing moves the wall clock too" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  kubectl_gate_output='deployment.apps/talos-image-cache'
  # After sourcing is the only moment a caller can turn these down, and it is how
  # the caller's own COZY_DIAG_* budgets are overridden. A wall-clock prefix built
  # at source time does not follow, so the inner --request-timeout drops to 2s
  # while the outer stays at the real 20s -- the drift the single value exists to
  # prevent, arriving through the knob meant to make it adjustable.
  _TALOS_IMAGE_CACHE_READ_TIMEOUT=2

  ( set +x; talos_image_cache_diagnose ) >"$tmp/out" 2>&1

  assert_file_contains '-k 5 2' "$timeout_calls"
  if grep -q -- '-k 5 20' "$timeout_calls"; then
    echo "FAIL: the wall-clock bound stayed at 20s while the request bound moved to 2s:" >&2
    grep -- '-k 5 20' "$timeout_calls" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a knob given a unit suffix or a leading zero is rejected and named rather than breaking the block" {
  # Each of these is pasted somewhere that takes digits and nothing else, and each
  # fails differently and quietly. The budget is the severe one: it reaches `$(( ))`
  # in the first statement of the block, so the arithmetic error unwinds the whole
  # function before its own headline and the failing run produces no diagnostics at
  # all -- the outcome this entire change exists to prevent, reached through a knob.
  # The cap's `[ n -gt 3s ]` exits 2 and the comparison reads false, so the cap
  # stops existing. The read bound becomes `--request-timeout=2ms`.
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  # 0480 alongside 8m: all digits, so a digits-only check passes it, and then the
  # budget dies in `$(( ))` as octal exactly as the suffix does while the read bound
  # quietly becomes 480 seconds. Same arm the previous-logs collector already has.
  # The grace is in this list because `timeout -k abc 20` exits 125 BEFORE running the
  # command, so a non-numeric grace makes every read in the block report exit 125 and
  # collects nothing -- the same total loss as the other three, by a different route.
  for knob in COZY_DIAG_PHASE_BUDGET COZY_DIAG_READ_TIMEOUT COZY_DIAG_MAX_IMPORTERS \
    COZY_DIAG_READ_GRACE; do
   # Only values a `:-` default cannot absorb. An empty value set here is
   # indistinguishable from unset, so it takes the default silently and correctly;
   # the empty-string hazard lives on the post-source path and is checked there.
   for bad in 8m 0480; do
    out=$(env "$knob=$bad" bash -c '
      set -eu
      . hack/e2e-chainsaw/_lib/run-kubernetes.sh
      cozy_report_guest_console_wedge() { :; }
      cozy_capture_tenant_serial_console() { :; }
      cozy_capture_tenant_talos() { :; }
      talos_image_cache_diagnose() { :; }
      cozy_capture_tenant_worker_cpu_throttle() { :; }
      cozy_capture_tenant_worker_network_counters() { :; }
      cozy_capture_sandbox_kvm_exits() { :; }
      cozy_capture_runner_kernel_cpu_time() { :; }
      cozy_capture_sandbox_qemu_thread_cpu() { :; }
      cozy_capture_runner_canary() { :; }
      ghcr_mirror_diagnose() { :; }
      kubectl() { :; }
      cozy_report_node_join_failure test-latest-version
    ' 2>&1) || true
    printf '%s\n' "$out" >"$tmp/$knob.$bad"
    if ! grep -q "ignoring $knob='$bad'" "$tmp/$knob.$bad"; then
      echo "FAIL: $knob=$bad was accepted instead of rejected and named" >&2
      cat "$tmp/$knob.$bad" >&2
      false
    fi
    # And the block still ran: a rejected knob falls back, it does not take the
    # diagnostics with it.
    if ! grep -q 'node-join failed: fewer than 2 tenant nodes Ready' "$tmp/$knob.$bad"; then
      echo "FAIL: $knob=$bad cost the block its diagnostics" >&2
      cat "$tmp/$knob.$bad" >&2
      false
    fi
   done
  done
  rm -rf "$tmp"
}

@test "with timeout off PATH the reads still run unbounded instead of all failing 127" {
  # The warning at phase start promises exactly this, and a promise the code does not
  # keep is the thing this whole change is against. Bounded-into-exit-127 is strictly
  # worse than the unbounded-but-working behaviour this path had before: eleven notes
  # blaming the cluster, and nothing read.
  #
  # A subprocess with a stripped PATH, because the suite mocks `timeout` as a shell
  # function and `command -v` finds functions -- the condition cannot be reached from
  # inside this file.
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  mkdir -p "$tmp/bin"
  # Resolved from known directories rather than through `command -v`: this file mocks
  # `date` as a shell function, so `command -v date` answers with the function name,
  # no symlink gets made, and the stripped PATH loses the clock along with `timeout`.
  #
  # `date` is staged so this test fails for its own reason or not at all. Losing it
  # under `set -eu` -- the mode the chainsaw script and the subshell below both use
  # -- takes exit 127 into the arithmetic in cozy_diag_phase_start and unwinds the
  # whole function at its first statement: the warning, one `date: command not
  # found`, and nothing else. That is not a scenario worth modelling either, because
  # `date +%s` is already required by the wait helpers this block sits after, so no
  # run reaches here without it.
  for c in date grep awk sed cat mktemp rm tr wc head mkdir; do
    for d in /bin /usr/bin /usr/local/bin /opt/homebrew/bin; do
      if [ -x "$d/$c" ]; then
        ln -sf "$d/$c" "$tmp/bin/$c"
        break
      fi
    done
  done
  if [ ! -x "$tmp/bin/date" ]; then
    echo "FAIL: could not stage a real date in the stripped PATH; the check below would be vacuous" >&2
    false
  fi
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    cozy_report_guest_console_wedge() { :; }
    cozy_capture_tenant_serial_console() { :; }
    cozy_capture_tenant_talos() { :; }
    talos_image_cache_diagnose() { :; }
    cozy_capture_tenant_worker_cpu_throttle() { :; }
    cozy_capture_tenant_worker_network_counters() { :; }
    cozy_capture_sandbox_kvm_exits() { :; }
    cozy_capture_runner_kernel_cpu_time() { :; }
    cozy_capture_sandbox_qemu_thread_cpu() { :; }
    cozy_capture_runner_canary() { :; }
    cozy_capture_tenant_worker_block_io() { :; }
    ghcr_mirror_diagnose() { :; }
    cozy_capture_sandbox_node_cpu_time() { :; }
    cozy_capture_tenant_worker_thread_cpu() { :; }
    kubectl() { printf "KUBECTL_RAN\n" >&2; }
    PATH='"$tmp"'/bin
    cozy_report_node_join_failure test-latest-version
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/out"

  ran=$(grep -c 'KUBECTL_RAN' "$tmp/out" || true)
  if [ "$ran" -lt 11 ]; then
    echo "FAIL: only $ran reads reached kubectl with timeout absent; expected every read to still run" >&2
    head -20 "$tmp/out" >&2
    false
  fi
  # And the block ran to its end. The count above saturates before the last
  # sections -- every collector past (d) is stubbed here, so it reads 11 whether
  # the block finished or died half way -- which is how an unguarded external
  # inside the sampling loop could land green. The final section's own header is
  # the one line printed only if nothing unwound the function on the way there,
  # and under `set -eu` a single `command not found` is enough to unwind it.
  assert_file_contains 're-probe talos-image-cache ClusterIP' "$tmp/out"
  assert_file_lacks_pattern 'command not found' "$tmp/out"
  # `sleep` is deliberately NOT staged above. It is the one external this block
  # calls outside a `|| true` and outside cozy_diag_read, so an unguarded call
  # exits 127 under `set -eu` and unwinds the function -- taking the collectors
  # after it, two of which the warning above promises keep collecting. Staging
  # it would make this test pass against exactly that defect, which is how it
  # went unnoticed once.
  assert_file_contains 'sleep is not on PATH' "$tmp/out"
  # And it is announced as a missing local dependency, not implied by the notes.
  assert_file_contains 'timeout is not on PATH' "$tmp/out"
  # Matched as a read's note rather than as the bare status: the phase's own warning
  # names exit 127 too, when it says which collectors do still fail that way, and a
  # substring match would find the warning and call it a failing read.
  if grep -q 'read failed (exit 127)' "$tmp/out"; then
    echo "FAIL: a read reported exit 127, i.e. it was bounded with a binary that is absent" >&2
    grep -n 'read failed (exit 127)' "$tmp/out" >&2
    false
  fi
  rm -rf "$tmp"
}

@test "a zero read bound is rejected because zero disables the bound instead of tightening it" {
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  # `timeout -k 5 0` does not time out at all and `--request-timeout=0s` means "no
  # timeout" to kubectl, so zero reintroduces the unbounded read this change removes.
  # The phase budget must keep taking zero -- the suite uses it as "already spent" --
  # so this is checked per knob rather than banned in the shared helper.
  out=$(env COZY_DIAG_READ_TIMEOUT=0 bash -c '
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    printf "resolved=%s\n" "$COZY_DIAG_READ_TIMEOUT"
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/read"
  assert_file_contains 'zero disables the bound' "$tmp/read"
  assert_file_contains 'resolved=20' "$tmp/read"

  # And after sourcing, which is the only moment a caller can adjust it and the case
  # the assignment-time check cannot see. Both halves of the pair have to follow: a
  # corrected `timeout` beside a stale `--request-timeout=0s` is the drift the single
  # value exists to prevent.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_DIAG_READ_TIMEOUT=0
    timeout() { printf "T: %s\n" "$*"; }
    kubectl() { :; }
    cozy_report_guest_console_wedge() { :; }
    cozy_capture_tenant_serial_console() { :; }
    cozy_capture_tenant_talos() { :; }
    talos_image_cache_diagnose() { :; }
    cozy_capture_tenant_worker_cpu_throttle() { :; }
    cozy_capture_tenant_worker_network_counters() { :; }
    cozy_capture_sandbox_kvm_exits() { :; }
    cozy_capture_runner_kernel_cpu_time() { :; }
    cozy_capture_sandbox_qemu_thread_cpu() { :; }
    cozy_capture_runner_canary() { :; }
    cozy_capture_tenant_worker_block_io() { :; }
    ghcr_mirror_diagnose() { :; }
    cozy_report_node_join_failure test-latest-version
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/postsource"
  assert_file_contains 'zero disables the bound' "$tmp/postsource"
  if grep -q -- '-k 5 0 ' "$tmp/postsource"; then
    echo "FAIL: a post-source zero left the wall clock unbounded" >&2
    grep -n -- '-k 5 0 ' "$tmp/postsource" >&2
    false
  fi
  if grep -q -- '--request-timeout=0s' "$tmp/postsource"; then
    echo "FAIL: a post-source zero left the per-request bound at 0s" >&2
    grep -n -- '--request-timeout=0s' "$tmp/postsource" >&2
    false
  fi

  # An empty value assigned after sourcing is the case a `:-` default cannot cover,
  # because the re-checks read the bare variable. Both validators have to reject it:
  # one of them let it through to `timeout -k 5 ''`, which exits 125 before running
  # the command and drops every read behind it.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    COZY_DIAG_READ_TIMEOUT=
    timeout() { printf "T: %s\n" "$*"; }
    kubectl() { :; }
    cozy_report_guest_console_wedge() { :; }
    cozy_capture_tenant_serial_console() { :; }
    cozy_capture_tenant_talos() { :; }
    talos_image_cache_diagnose() { :; }
    cozy_capture_tenant_worker_cpu_throttle() { :; }
    cozy_capture_tenant_worker_network_counters() { :; }
    cozy_capture_sandbox_kvm_exits() { :; }
    cozy_capture_runner_kernel_cpu_time() { :; }
    cozy_capture_sandbox_qemu_thread_cpu() { :; }
    cozy_capture_runner_canary() { :; }
    cozy_capture_tenant_worker_block_io() { :; }
    ghcr_mirror_diagnose() { :; }
    cozy_report_node_join_failure test-latest-version
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/empty"
  assert_file_contains 'a bare integer, no unit suffix' "$tmp/empty"
  assert_file_contains 'T: -k 5 20 ' "$tmp/empty"

  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_READ_TIMEOUT=
    timeout() { printf "T: %s\n" "$*"; }
    kubectl() { case "$*" in *"get deploy talos-image-cache"*) printf "dep\n" ;; esac; }
    _talos_image_cache_reachable_from_tenant() { return 1; }
    talos_image_cache_diagnose
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/empty-cache"
  assert_file_contains 'a bare integer, no unit suffix' "$tmp/empty-cache"
  assert_file_contains 'T: -k 5 20 ' "$tmp/empty-cache"

  # The cache grace, on the same post-source path, and it needs its own case for two
  # reasons: one helper validates both knobs there, so a message can name the wrong
  # one, and the grace's own hazard is different -- `timeout -k abc 20` exits 125
  # before running the command, which drops every dump and the gate with them.
  out=$(bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    _TALOS_IMAGE_CACHE_READ_GRACE=abc
    timeout() { printf "T: %s\n" "$*"; }
    kubectl() { case "$*" in *"get deploy talos-image-cache"*) printf "dep\n" ;; esac; }
    _talos_image_cache_reachable_from_tenant() { return 1; }
    talos_image_cache_diagnose
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/grace-cache"
  # Named for the knob the caller set, not for its sibling: the first version of that
  # helper hardcoded one name into every message and quoted the other's default.
  assert_file_contains "ignoring _TALOS_IMAGE_CACHE_READ_GRACE='abc'" "$tmp/grace-cache"
  if grep -q -- '-k abc ' "$tmp/grace-cache"; then
    echo "FAIL: an invalid grace reached timeout, so every dump exits 125" >&2
    grep -n -- '-k abc ' "$tmp/grace-cache" >&2
    false
  fi
  # And zero stays legal for a grace, which is what its comment claims: -k 0 only
  # skips the follow-up SIGKILL, and kubectl does not need it.
  out=$(bash -c '
    set -eu
    _TALOS_IMAGE_CACHE_READ_GRACE=0
    . hack/e2e-chainsaw/_lib/talos-image-cache.sh
    printf "grace=%s\n" "$_TALOS_IMAGE_CACHE_READ_GRACE"
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/grace-zero"
  assert_file_contains 'grace=0' "$tmp/grace-zero"

  out=$(env COZY_DIAG_PHASE_BUDGET=0 bash -c '
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    printf "resolved=%s\n" "$COZY_DIAG_PHASE_BUDGET"
  ' 2>&1) || true
  printf '%s\n' "$out" >"$tmp/budget"
  assert_file_contains 'resolved=0' "$tmp/budget"
  rm -rf "$tmp"
}

@test "the decisive CSR reads run before the collectors that can exhaust the budget" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The phase budget declines whatever has not started when it runs out, so
  # source order decides what gets dropped. Section (c) is the discriminator
  # between a worker that booted and never registered and one that never booted
  # -- per the block's own header, the failure a follow-up fix has to target --
  # and it is two bounded reads. The serial-console walk and the guest-Talos
  # capture can spend the whole budget between them. With
  # (c) last, the budget declines the answer and keeps the noise.
  csr=$(grep -n "cozy_diag_read 'tenant CSR list'" "$lib" | head -n 1 | cut -d: -f1)
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  talos=$(grep -n 'cozy_capture_tenant_talos "${test_name}" || true' "$lib" | head -n 1 | cut -d: -f1)
  for v in csr console talos; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      exit 1
    fi
  done
  if [ "$csr" -ge "$console" ] || [ "$csr" -ge "$talos" ]; then
    echo "the CSR reads (line $csr) must precede the serial console ($console) and the Talos capture ($talos)" >&2
    exit 1
  fi
}

@test "the replicated volume state is read before anything that can spend the budget" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # One read, and it answers a question with no other source once the suite ends:
  # the report collects the same tables after cleanup has deleted the worker's
  # volumes, so a red run leaves nothing about their state behind. A DRBD replica
  # serving the worker's system disk from a peer rather than locally is slow in a
  # way every counter in this block attributes to something else, so a budget that
  # declines this read keeps every symptom and drops the cause.
  volumes=$(grep -n "cozy_diag_read 'LINSTOR resource state'" "$lib" | head -n 1 | cut -d: -f1)
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  cpu=$(grep -n 'cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true' "$lib" | head -n 1 | cut -d: -f1)
  talos=$(grep -n 'cozy_capture_tenant_talos "${test_name}" || true' "$lib" | head -n 1 | cut -d: -f1)
  mirror=$(grep -n 'ghcr_mirror_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  cache=$(grep -n 'talos_image_cache_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  for v in volumes console cpu talos mirror cache; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      exit 1
    fi
  done
  for later in console cpu talos mirror cache; do
    eval "n=\$$later"
    if [ "$volumes" -ge "$n" ]; then
      echo "the volume state read (line $volumes) must precede $later (line $n), or a tight run declines it" >&2
      exit 1
    fi
  done
}

@test "the block IO counters run with the cheap reads, ahead of everything that costs minutes" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Same decision as the network counters beside it and the same shape: one
  # listing and one read per node, read once, against collectors that can spend
  # minutes each. What makes it worth a position rather than a place is that
  # nothing else here counts a read at all -- the CPU families say what the group
  # was allowed and what it got, the byte counters say what crossed the network,
  # and a guest waiting on blocks looks exactly like a guest computing without
  # result in both. Below the guest captures it would be declined on the slow runs
  # this failure comes from, which are the only runs it exists for.
  block=$(grep -n 'cozy_capture_tenant_worker_block_io || true' "$lib" | head -n 1 | cut -d: -f1)
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  cpu=$(grep -n 'cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true' "$lib" | head -n 1 | cut -d: -f1)
  talos=$(grep -n 'cozy_capture_tenant_talos "${test_name}" || true' "$lib" | head -n 1 | cut -d: -f1)
  mirror=$(grep -n 'ghcr_mirror_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  cache=$(grep -n 'talos_image_cache_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  for v in block console cpu talos mirror cache; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      exit 1
    fi
  done
  for later in console cpu talos mirror cache; do
    eval "n=\$$later"
    if [ "$block" -ge "$n" ]; then
      echo "the block IO counters (line $block) must precede $later (line $n), or a tight run declines them" >&2
      exit 1
    fi
  done
}

@test "the byte-path counters run with the cheap reads, ahead of everything that costs minutes" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Where this collector sits is a decision, not a placement, and the phase
  # budget is what makes it one: admission gates when a collector may START, so
  # what runs last is what gets declined, and a collector's position is its
  # priority.
  #
  # It is first of the gated collectors, and it is there because it is the
  # cheapest of them: one listing and one read per node against the minutes the
  # console walk, the guest capture and the CPU pair can each spend. Its answer
  # exists nowhere else in the artifact either -- the bytes that arrived at the
  # Pod, set against the progress the guest reported, separate a pull that kept
  # restarting from a slow link, and no other collector here separates those
  # two. Which of the Pod's four interfaces carries that number, and which one
  # is the dummy that reads zero, is stated where the collector is gated. Placed
  # after the guest captures it would be the first thing declined on exactly the
  # slow runs that produce this failure.
  #
  # It goes AHEAD of the CPU pair, which is a stronger claim than the one it
  # replaces. The two used to be the same size, so the order between them was
  # nearly arbitrary; the pair now walks twice and can spend most of the budget,
  # so cost decides it rather than taste. The companion guard is the one that
  # bounds what may sit ahead of the console at all.
  net=$(grep -n 'cozy_capture_tenant_worker_network_counters || true' "$lib" | head -n 1 | cut -d: -f1)
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  cpu=$(grep -n 'cozy_capture_tenant_worker_cpu_throttle "${_sample}" || true' "$lib" | head -n 1 | cut -d: -f1)
  talos=$(grep -n 'cozy_capture_tenant_talos "${test_name}" || true' "$lib" | head -n 1 | cut -d: -f1)
  mirror=$(grep -n 'ghcr_mirror_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  cache=$(grep -n 'talos_image_cache_diagnose || true' "$lib" | head -n 1 | cut -d: -f1)
  for v in cpu net console talos mirror cache; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to locate $v in $lib" >&2
      exit 1
    fi
  done
  for later in console cpu talos mirror cache; do
    eval "n=\$$later"
    if [ "$net" -ge "$n" ]; then
      echo "the network counters (line $net) must precede $later (line $n), or a tight run declines them" >&2
      exit 1
    fi
  done
}

@test "nothing gated ahead of the console can spend the phase budget by itself" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The budget guard below holds the phase against the op. This one holds the
  # ORDER against the collector the phase exists to reach. Admission is "the
  # budget is not spent yet", so whatever is gated ahead of the serial console
  # bounds what the console can lose, and the console is documented as the only
  # surface that survives a worker which never reached apid -- the dominant
  # shape of this failure. A collector below it can only cost itself; one above
  # it can cost the console.
  #
  # Two claims, because one of them is structural and the other is arithmetic
  # and neither implies the other.
  console=$(grep -n "cozy_capture_tenant_serial_console 'node-join failed" "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$console" ]; then
    echo "expected the failure path to capture the guest serial console" >&2
    return 1
  fi
  # Structural: a collector read more than once costs its whole walk again, and
  # the interval on top. That shape belongs below the console whatever its
  # arithmetic says today, because the arithmetic is what changes when a node is
  # added to the sandbox.
  loop=$(grep -n '^ *for _sample in 1 2; do$' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$loop" ]; then
    echo "expected the sampling loop to still exist in $lib" >&2
    return 1
  fi
  if [ "$loop" -lt "$console" ]; then
    echo "the sampling loop (line $loop) is gated ahead of the console (line $console); a collector that walks twice must not bound what the console gets" >&2
    return 1
  fi
  # Arithmetic: what IS ahead of it must not be able to spend the whole budget
  # on its own. This is deliberately not "ahead + console <= budget", which the
  # tree has never satisfied and did not at the merge base either: the console's
  # own worst case plus one four-read walk already exceeds the budget, so a
  # tight run has always been able to cut the console short. What must not
  # happen is a collector ahead of it that can spend the budget outright, which
  # turns "cut short" into "never started".
  budget=$(grep -oE '^COZY_DIAG_PHASE_BUDGET_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  bound=$(grep -oE '^COZY_DIAG_READ_TIMEOUT_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  grace=$(grep -oE '^COZY_DIAG_READ_GRACE_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  # The canary declares a ceiling of its own rather than taking the read bound,
  # so its arm below is priced from that number and not from `bound`. Read from
  # the source for the reason every other input to this guard is: a ceiling
  # raised there and left alone here would be summed at the old figure while the
  # guard went on reading as satisfied.
  canary=$(grep -oE '^COZY_CANARY_RUN_BOUND_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  for v in budget bound grace canary; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  # Every walk's cap, not the first one, and not only the ones spelled
  # `max_nodes`: the subjects declare their own literal and two of them are
  # equal today, so taking the first would be right by accident, and a subject
  # that walks Pods rather than nodes would be left out of the sum entirely --
  # under-counting the pair while reading as satisfied.
  #
  # A literal cap only. The serial-console walk takes its cap from its caller,
  # which is what keeps it out of this expression: it is not part of the pair.
  caps=$(grep -oE 'local max_(nodes|pods)=[0-9]+' "$lib" | sed -E 's/.*=//')
  # `|| true`: grep -c exits 1 on a count of zero, and under errexit that ends
  # the test before the branch below can say what was missing -- the guard would
  # still fail, with the diagnostic it was given replaced by silence.
  ncaps=$(printf '%s\n' "$caps" | grep -c . || true)
  if [ "$ncaps" -ne 3 ]; then
    echo "expected three capped walks in $lib, found $ncaps; the pair arithmetic below is written for exactly the subjects the sampling loop reads" >&2
    return 1
  fi
  # A capped walk costs a listing plus one read per node, each at the read bound
  # plus its kill grace. Taken from the shared cAdvisor body by name rather than
  # as the first cap in the file: everything counted into `ahead` below runs
  # through that body, and picking by position would be right only while it
  # happens to be declared first.
  cap=$(awk '/^_cozy_capture_worker_cadvisor\(\)/,/^}/' "$lib" \
    | grep -oE 'local max_nodes=[0-9]+' | head -n 1 | sed -E 's/.*=//')
  if [ -z "$cap" ]; then
    echo "expected the shared cAdvisor walk to declare its own cap; without it this guard reports success for having lost its input" >&2
    return 1
  fi
  walk=$(( (1 + cap) * (bound + grace) ))
  ahead=0
  gated=$(grep -nE '^ *(cozy_(capture|report)_[a-z_]+|[a-z_]+_diagnose) [^|]*\|\| true|^ *(cozy_(capture|report)_[a-z_]+|[a-z_]+_diagnose) \|\| true' "$lib" \
    | sed -E 's/:[[:space:]]*/:/; s/(:[a-z_]+) .*/\1/; s/\|\|.*//')
  # Checked for emptiness like every other input here, and this one matters
  # most: the loop below fails on a collector gated ahead of the console that
  # has no cost arm, which is what catches the next heavy collector being put
  # there. With no input the loop runs zero times, `ahead` stays 0, and both
  # arms pass while nothing has been looked at. The input is a grep over call
  # spellings, and this branch changed one of those spellings.
  if [ -z "$gated" ]; then
    echo "found no gated collector at all, so this guard checked nothing" >&2
    return 1
  fi
  for entry in $gated; do
    line=${entry%%:*}
    fn=${entry#*:}
    [ "$line" -lt "$console" ] || continue
    case "$fn" in
      # Not behind the phase gate at all: it runs ahead of the headline so the
      # console experiment's own failure is named before the wording that
      # matches the bug it studies, and it is two bounded reads.
      cozy_report_guest_console_wedge) continue ;;
      # Not behind the phase gate either, and for a reason of its own: it is the
      # second half of a pair whose first reading was taken before the node-join
      # wait, so declining it would not save a reading, it would orphan one
      # already taken. Counted rather than skipped all the same -- it does sit
      # ahead of the console in wall clock, and one bounded read is a cost this
      # arm can state exactly, unlike a walk whose size follows the sandbox.
      cozy_capture_sandbox_kvm_exits) ahead=$((ahead + bound + grace)) ;;
      # The other two halves of the same shape, and priced identically. The
      # runner-kernel reading is one bounded read of /proc/stat; the QEMU
      # thread reading is one bounded probe walk of this container's own /proc
      # plus three bounded pid-file reads, all under the same ceiling the arm
      # prices. Each pair's first half is taken before the wait, so declining
      # either here would orphan a reading taken eighteen minutes earlier
      # rather than save one. Counted all the same, because they do sit ahead
      # of the console in wall clock.
      cozy_capture_runner_kernel_cpu_time) ahead=$((ahead + bound + grace)) ;;
      cozy_capture_sandbox_qemu_thread_cpu) ahead=$((ahead + bound + grace)) ;;
      # The fourth reading that brackets the wait, and the only one of them that
      # is not a read: two arms of fixed work, each under the canary ceiling, so
      # it is priced at two of those rather than at the read bound the three
      # above it share. Exempt on a ground of its own rather than theirs: its
      # samples stand alone, so nothing is orphaned by declining this one, but
      # the figure is only worth what its nearness to the failure makes it, and
      # the gate can only decline or defer. Counted all the same, because it
      # does sit ahead of the console in wall clock.
      cozy_capture_runner_canary) ahead=$((ahead + 2 * (canary + grace))) ;;
      cozy_capture_tenant_worker_network_counters) ahead=$((ahead + walk)) ;;
      # One walk apiece and read once, so each costs what the expression above
      # prices a capped walk at. Both sit ahead of the console on the same
      # ground: their answers exist nowhere else in the artifact, and a walk read
      # once cannot cost the console more than a walk's worth.
      cozy_capture_tenant_worker_block_io) ahead=$((ahead + walk)) ;;
      *)
        echo "$fn is gated ahead of the console and this guard has no cost arm for it; add one, or move it below the console" >&2
        return 1
        ;;
    esac
  done
  if [ "$ahead" -gt "$budget" ]; then
    echo "the collectors gated ahead of the console can spend ${ahead}s against a ${budget}s budget, so the console can be declined rather than merely cut short" >&2
    return 1
  fi
  # And the pair's own overshoot stays inside the term the budget was derived
  # against. The phase is start-gated, so a collector admitted a moment before
  # the deadline runs its whole cost afterwards; the derivation below covers
  # exactly one such overshoot, sized from the heaviest collector. A pair
  # heavier than that term would push the tenant snapshot past the op while the
  # inequality still read as satisfied, because the inequality does not know
  # which collector is heaviest. Read from the guard that owns it rather than
  # restated, since two copies of a number are a scheduled divergence.
  interval=$(grep -oE '^COZY_DIAG_RATE_INTERVAL_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  # Named rather than taken from $0: cozytest.sh dot-sources a transformed copy
  # of this file, so $0 is the runner there and the bats binary here, and the
  # two runners would read different files.
  largest=$(grep -oE '^  largest=[0-9]+' hack/run-kubernetes-node-join_test.bats | head -n 1 | sed -E 's/.*=//')
  for v in interval largest; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v; without it this half of the guard reports success for having lost its input" >&2
      return 1
    fi
  done
  # Each subject, two samples each, one walk apiece at that subject's own cap,
  # plus the single wait -- single because the subjects share the pass rather
  # than each waiting for their own. Summed per subject rather than multiplying
  # one walk, so a cap raised on one side alone is counted where it lands.
  pair=$interval
  for c in $caps; do
    pair=$(( pair + 2 * (1 + c) * (bound + grace) ))
  done
  if [ "$pair" -gt "$largest" ]; then
    echo "the sampling pair can overshoot by ${pair}s against the ${largest}s the budget derivation allows for one late admission; the snapshot the phase exists to reach no longer fits behind it" >&2
    return 1
  fi
}

@test "every collector that survives a missing timeout is named in the warning that says so" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The warning an operator reads when `timeout` is absent splits the collectors
  # into the ones that then exit 127 and collect nothing and the ones that run
  # unbounded instead, and it names the second group. An enumeration is only
  # true until the next collector joins the group, and a reader who trusts a
  # stale one draws the opposite conclusion about whichever collector is
  # missing from it. So the list is derived here rather than restated: every
  # function that guards its bounded call with `command -v timeout` has to
  # appear in that sentence.
  # Anchored on the phrase unique to the phase warning itself: the captures now
  # write their own [bounds] notes carrying "timeout is not on PATH", so the
  # first match of that phrase is no longer this sentence.
  warn=$(grep -n 'run UNBOUNDED' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$warn" ]; then
    echo "expected the phase to still warn when timeout is missing" >&2
    exit 1
  fi
  line=$(sed -n "${warn}p" "$lib")
  # The guarded collectors, found by walking the source: a `command -v timeout`
  # test inside a function body puts that function in the group. awk tracks the
  # enclosing definition so the name reported is the function's, not the line's.
  #
  # Over the sourced libraries as well as this file, and the list of them is
  # read from the source rather than written out here. Two of the four captures
  # the sentence names -- the ghcr-mirror and talos-image-cache diagnoses --
  # live in libraries this file sources, so a scan of this file alone finds
  # neither, and their arms in the table below sat unreachable while reading as
  # coverage. A guarded read added to any sourced library is what this has to
  # see, and hardcoding today's two would put the next library outside the scan
  # in exactly the same silent way.
  libs="$lib $(awk '/^\. hack\/e2e-chainsaw\/_lib\/[a-z-]+\.sh$/ { print $2 }' "$lib")"
  for f in $libs; do
    if [ ! -f "$f" ]; then
      echo "the scan was pointed at $f, which does not exist" >&2
      exit 1
    fi
  done
  # Two directions, because the sentence goes stale two ways.
  #
  # `guarded` is the set of functions that carry the guard, read off the source.
  # The `^}` reset matters: without it a guard appearing at top level after a
  # definition is filed under the preceding function, which is a wrong name in
  # the group rather than a missing one.
  #
  # What is deliberately NOT here is a call-graph closure from each capture down
  # to a guarded read. Deriving one from shell text means matching callee names
  # inside bodies that also contain comments and strings, and a matcher loose
  # enough to find the calls reports functions that make none -- green while
  # naming nothing. A guard that has to parse shell to be right is a guard that
  # will be wrong; the explicit table below says the same thing in a form that
  # cannot drift silently, because every name in it must appear in `guarded` or
  # the test fails.
  guarded=$(awk '
    /^[a-z_]+\(\) *[({] *$/ { fn = $0; sub(/\(\).*/, "", fn); next }
    /^[})]$/ { fn = ""; next }
    /command -v timeout >\/dev\/null 2>&1; then/ { if (fn != "") print fn }
  ' $libs | sort -u)
  if [ -z "$guarded" ]; then
    echo "found no function carrying the timeout guard, so this test checked nothing" >&2
    exit 1
  fi
  # Direction one: for each capture the sentence names, the functions whose
  # guard is what makes that sentence true of it. The cAdvisor captures do
  # not carry the guard themselves -- it lives in the body they share -- and
  # naming that body here is what the closure was trying and failing to derive.
  # A helper renamed or a guard removed drops it out of `guarded`, and this
  # fails; a capture dropped from the warning fails on the phrase check below.
  for entry in \
    'cozy_capture_tenant_worker_cpu_throttle:CPU throttling:_cozy_cadvisor_node_stream _cozy_virt_launcher_listing' \
    'cozy_capture_tenant_worker_network_counters:network counter:_cozy_cadvisor_node_stream _cozy_virt_launcher_listing' \
    'cozy_capture_tenant_worker_block_io:block IO counter:_cozy_cadvisor_node_stream _cozy_virt_launcher_listing' \
    'cozy_capture_sandbox_node_cpu_time:sandbox node CPU time:cozy_capture_sandbox_node_cpu_time' \
    'cozy_capture_sandbox_kvm_exits:sandbox kernel KVM counters:cozy_capture_sandbox_kvm_exits' \
    'cozy_capture_runner_kernel_cpu_time:runner kernel CPU time:cozy_capture_runner_kernel_cpu_time' \
    'cozy_capture_sandbox_qemu_thread_cpu:sandbox QEMU per-thread CPU time:cozy_capture_sandbox_qemu_thread_cpu' \
    'cozy_capture_runner_canary:runner fixed-work canary:_cozy_canary_run_arm' \
    'cozy_capture_tenant_worker_thread_cpu:worker per-thread CPU time:cozy_capture_tenant_worker_thread_cpu _cozy_virt_launcher_listing' \
    'ghcr_mirror_diagnose:ghcr-mirror:ghcr_mirror_diagnose _ghcr_mirror_bounded_read' \
    'talos_image_cache_diagnose:talos-image-cache:talos_image_cache_diagnose _talos_image_cache_bounded_read'; do
    fn=${entry%%:*}
    rest=${entry#*:}
    phrase=${rest%%:*}
    carriers=${rest#*:}
    for carrier in $carriers; do
      case "
${guarded}
" in
        *"
${carrier}
"*) ;;
        *)
          echo "$fn is named in the warning as surviving a missing timeout, but $carrier carries no command -v guard; the sentence is claiming behaviour the code no longer has" >&2
          exit 1
          ;;
      esac
    done
    case "$line" in
      *"$phrase"*) ;;
      *)
        echo "the warning does not name $fn (expected the phrase: $phrase)" >&2
        exit 1
        ;;
    esac
  done
  # Direction two: every function carrying the guard is either one of those
  # captures or exempt for a stated reason. One with no entry here fails rather
  # than being skipped, because a silent skip is how the enumeration went stale.
  for fn in $guarded; do
    case "$fn" in
      cozy_capture_tenant_worker_cpu_throttle) phrase='CPU throttling' ;;
      cozy_capture_tenant_worker_network_counters) phrase='network counter' ;;
      cozy_capture_sandbox_node_cpu_time) phrase='sandbox node CPU time' ;;
      cozy_capture_sandbox_kvm_exits) phrase='sandbox kernel KVM counters' ;;
      cozy_capture_runner_kernel_cpu_time) phrase='runner kernel CPU time' ;;
      cozy_capture_sandbox_qemu_thread_cpu) phrase='sandbox QEMU per-thread CPU time' ;;
      # In `guarded` twice over: the body that runs one arm guards the ceiling
      # it puts around the work, and the capture guards a second check of its
      # own to decide whether to write the [bounds] note. Both belong to the one
      # collector the sentence names, so the phrase here answers for the pair
      # and the arm body is exempted below rather than given a phrase of its own.
      cozy_capture_runner_canary) phrase='runner fixed-work canary' ;;
      cozy_capture_tenant_worker_thread_cpu) phrase='worker per-thread CPU time' ;;
      ghcr_mirror_diagnose) phrase='ghcr-mirror' ;;
      talos_image_cache_diagnose) phrase='talos-image-cache' ;;
      # The sentence enumerates the CAPTURES. Two other kinds of function guard
      # the same way and are deliberately not in it, each exempt for a stated
      # reason rather than by omission: the shared bounded-read helpers, and the
      # individual reads belonging to a capture the sentence already names.
      # Both are covered by the comment directly above the warning, and putting
      # them in the sentence too would be a second copy of that claim to keep in
      # step. A function that is neither still fails below, which is what makes
      # this a list of exemptions rather than a list of everything.
      cozy_diag_read | _ghcr_mirror_bounded_read | _talos_image_cache_bounded_read) continue ;;
      _cozy_cadvisor_node_stream | _cozy_virt_launcher_listing) continue ;;
      # The canary carries its guard in the body that runs one arm, the way the
      # cAdvisor captures carry theirs in the stream they share, so the sentence
      # names the capture and the table above names what makes it true of it.
      _cozy_canary_run_arm) continue ;;
      cozy_report_node_join_failure | _talos_image_cache_deploy_state) continue ;;
      *)
        echo "$fn guards its call with command -v but this test has no phrase for it; add one here and to the warning" >&2
        exit 1
        ;;
    esac
    case "$line" in
      *"$phrase"*) ;;
      *)
        echo "the warning does not name $fn (expected the phrase: $phrase)" >&2
        exit 1
        ;;
    esac
  done
}

@test "the spend order both chainsaw suites document matches the order the block runs" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Those comments are where a reader checks what the phase budget covers and in
  # what order it is spent, which makes them the one place a collector added to
  # the phase is most useful and most easily forgotten -- the ceiling line four
  # lines above gets edited because a test compares it, and the list below it
  # did not because nothing did. This is that nothing.
  #
  # Position, not just presence. The list's whole subject is the ORDER, since
  # what a spent budget declines is whatever has not started; a presence check
  # would accept the new collector appended at the bottom next to the two
  # collectors it is deliberately ahead of, and the comment would then describe
  # the opposite of what the block does.
  #
  # Both sides are derived. The call order comes from the source, and each
  # collector with no entry in the phrase table fails here rather than being
  # skipped, which is how the list went stale in the first place.
  #
  # Keyed on the `|| true` call suffix rather than on cozy_diag_phase_has_time,
  # and the two sets are not identical: cozy_report_guest_console_wedge is
  # called that way and is NOT behind the gate, which is why it needs an arm of
  # its own below. The call suffix is what this test can order; the gate is a
  # separate line from the call it guards.
  gated=$(grep -nE '^ *(cozy_(capture|report)_[a-z_]+|[a-z_]+_diagnose) [^|]*\|\| true|^ *(cozy_(capture|report)_[a-z_]+|[a-z_]+_diagnose) \|\| true' "$lib" \
    | sed -E 's/:[[:space:]]*/:/; s/(:[a-z_]+) .*/\1/; s/\|\|.*//')
  if [ -z "$gated" ]; then
    echo "found no gated collector at all, so this test checked nothing" >&2
    exit 1
  fi
  expected=
  for entry in $gated; do
    fn=${entry#*:}
    case "$fn" in
      cozy_capture_tenant_worker_cpu_throttle) phrase='worker CPU usage and throttling counters' ;;
      cozy_capture_sandbox_node_cpu_time) phrase='sandbox node CPU time' ;;
      cozy_capture_tenant_worker_thread_cpu) phrase='worker per-thread CPU time' ;;
      cozy_capture_tenant_worker_network_counters) phrase='worker network counters' ;;
      cozy_capture_tenant_worker_block_io) phrase='worker block IO counters' ;;
      cozy_capture_tenant_serial_console) phrase='serial-console family' ;;
      cozy_capture_tenant_talos) phrase='guest Talos capture' ;;
      ghcr_mirror_diagnose) phrase='ghcr-mirror state' ;;
      talos_image_cache_diagnose) phrase='talos-image-cache diagnosis' ;;
      # Called with the same suffix but not behind the phase gate: it runs ahead
      # of the headline so the console experiment's own failure is named before
      # the wording that matches the bug it studies, and it is not part of what
      # the budget covers.
      cozy_report_guest_console_wedge) continue ;;
      # Called the same way and also not behind the gate. Its first reading was
      # taken before the node-join wait, so it is not something the budget hands
      # out or withholds: declining it would leave that earlier reading with
      # nothing to pair against rather than save the phase any time.
      cozy_capture_sandbox_kvm_exits) continue ;;
      # The other two halves of that same pair, exempt on the same ground and not
      # on their own: each was read once before the node-join wait, so what runs
      # here is the second half of a measurement rather than a collector the
      # budget may hand out or withhold.
      cozy_capture_runner_kernel_cpu_time) continue ;;
      cozy_capture_sandbox_qemu_thread_cpu) continue ;;
      # The fourth bracket half, and also not behind the gate.
      cozy_capture_runner_canary) continue ;;
      *)
        echo "$fn runs in the diagnostics block but this test has no phrase for it; add one here and to both chainsaw comments" >&2
        exit 1
        ;;
    esac
    expected="${expected}${phrase}
"
  done
  for f in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
    hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
    actual=
    while IFS= read -r phrase; do
      [ -n "$phrase" ] || continue
      line=$(grep -n "$phrase" "$f" | head -n 1 | cut -d: -f1)
      if [ -z "$line" ]; then
        echo "$f does not list the spend-order entry: $phrase" >&2
        exit 1
      fi
      actual="${actual}${line} ${phrase}
"
    done <<EOF
$expected
EOF
    # Sorting the located lines numerically must reproduce the call order. A
    # mismatch means the comment and the block disagree about which collector
    # the budget gives up first, which is the only thing this list is read for.
    if [ "$(printf '%s' "$actual" | sort -n | sed -E 's/^[0-9]+ //')" != "$(printf '%s' "$actual" | sed -E 's/^[0-9]+ //')" ]; then
      echo "$f lists the collectors in a different order than the block runs them" >&2
      printf '%s\n' "$actual" >&2
      exit 1
    fi
  done
}

@test "nothing new runs between the KVM reading and the two beside it" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The interval each of the three pairs divides by is whatever runs between its
  # two readings, and on the failure path that is everything the block does before
  # the second one. The KVM guard below pins what may precede ITS reading; this
  # pins the gap between that reading and the two that follow it, which is where
  # the other two intervals actually widen. Nothing belongs in there: the three
  # readings are the second halves of three pairs and are meant to be adjacent.
  block=$(grep -n '^cozy_report_node_join_failure() {' "$lib" | head -n 1 | cut -d: -f1)
  kvm=$(awk -v b="$block" 'NR > b && /cozy_capture_sandbox_kvm_exits 2/ { print NR; exit }' "$lib")
  last=$(awk -v b="$block" 'NR > b && /cozy_capture_sandbox_qemu_thread_cpu 2/ { print NR; exit }' "$lib")
  for v in block kvm last; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$last" -le "$kvm" ]; then
    echo "expected the QEMU thread reading (line $last) to follow the KVM one (line $kvm) inside the failure block" >&2
    return 1
  fi
  # Matched on the call rather than on the `|| true` suffix, for the reason the
  # KVM guard gives: the suffix says a call is phase-gated and says nothing about
  # what costs time, and the dominant idiom in this block is `cozy_diag_read`
  # with no suffix at all.
  between=$(grep -nE '^ *(cozy_capture_[a-z_]+|cozy_report_[a-z_]+|cozy_diag_read|[a-z_]+_diagnose)( |$)' "$lib" \
    | sed -E 's/:[[:space:]]*/:/; s/(:[a-z_]+) .*/\1/' \
    | awk -F: -v a="$kvm" -v b="$last" '$1 > a && $1 < b { print $2 }')
  case "$(printf '%s\n' $between)" in
    'cozy_capture_runner_kernel_cpu_time') ;;
    *)
      echo "more than the runner kernel reading now runs between the KVM reading and the QEMU thread one, so those two intervals are wider than the wait by whatever it costs:" >&2
      printf '%s\n' $between >&2
      return 1
      ;;
  esac
}

@test "the KVM counter readings sit on either side of the node-join wait on both paths" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # What this pair measures is decided by where its two calls sit, not by
  # anything inside the collector: the counters carry running totals, so the
  # interval a difference divides by is whatever runs between the readings.
  #
  # The failure path gets that for free -- the second reading is among the first
  # things the failure block does, so the interval is the wait. The passing path
  # does not. Everything from the kubelet-version check through the storage and
  # LoadBalancer assertions runs after the wait, and the comment above the wait
  # prices that tail at ten to fifteen minutes, so a second reading placed after
  # it prices a different window over a different workload while the capture's
  # own legend says it priced the join. Since the green run is what the red ones
  # are read against, that does not merely widen the green number, it removes
  # the comparison the collector exists for.
  wait_line=$(grep -n '^  if ! timeout 18m bash -c' "$lib" | head -n 1 | cut -d: -f1)
  first=$(grep -n 'cozy_capture_sandbox_kvm_exits 1' "$lib" | head -n 1 | cut -d: -f1)
  # The green second reading, found as the one after the wait rather than by
  # position in the file: the other call with the same argument is the failure
  # block's, and picking by count would be right only while the two happen to be
  # written in this order.
  green=$(awk -v w="$wait_line" 'NR > w && /cozy_capture_sandbox_kvm_exits 2/ { print NR; exit }' "$lib")
  # The first thing the happy path does with the tenant cluster once the wait
  # returns. It stands in for the whole tail rather than for itself.
  tail_line=$(grep -n '^  versions=\$(kubectl --kubeconfig' "$lib" | head -n 1 | cut -d: -f1)
  for v in wait_line first green tail_line; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$first" -ge "$wait_line" ]; then
    echo "the first KVM counter reading (line $first) is not taken before the node-join wait (line $wait_line), so the pair does not span it" >&2
    return 1
  fi
  if [ "$green" -le "$wait_line" ]; then
    echo "the passing path's second KVM counter reading (line $green) is not after the wait (line $wait_line)" >&2
    return 1
  fi
  if [ "$green" -ge "$tail_line" ]; then
    echo "the passing path's second KVM counter reading (line $green) is taken after the suite's happy-path tail begins (line $tail_line), so the green interval covers minutes of work the red one does not and the two are no longer comparable" >&2
    return 1
  fi
  # And the same claim on the failing side, which is the half that has no
  # equivalent of the tail to fail against and so would otherwise widen in
  # silence. The failure block's own reads are bounded and small against an 18m
  # wait, so this is not about today's arithmetic; it is that a collector
  # inserted ahead of the reading later moves the red interval while the
  # symmetric mistake on the green side fails loudly.
  red=$(awk -v w="$wait_line" 'NR < w && /cozy_capture_sandbox_kvm_exits 2/ { line = NR } END { if (line) print line }' "$lib")
  node_table=$(grep -n "cozy_diag_read 'tenant node table'" "$lib" | head -n 1 | cut -d: -f1)
  for v in red node_table; do
    eval "n=\$$v"
    if [ -z "$n" ]; then
      echo "expected to read $v from $lib; without it this half of the guard reports success for having lost its input" >&2
      return 1
    fi
  done
  if [ "$red" -ge "$node_table" ]; then
    echo "the failure path's second KVM counter reading (line $red) is taken after the diagnostics block has started reading the cluster (line $node_table), so the red interval is the wait plus whatever those reads cost" >&2
    return 1
  fi
  # And what is allowed to run in front of it, by name. The check above bounds
  # the reading against one later line; it does not notice a collector inserted
  # between the block's first line and the reading, which is the way the red
  # interval actually widens. One collector is there on purpose -- the wedge
  # check, which has to name the console experiment's own failure before the
  # headline whose wording matches the bug it studies -- and it is two bounded
  # reads. Anything joining it has to be a decision rather than a diff nobody
  # priced, so the set is pinned to that one name.
  block=$(grep -n '^cozy_report_node_join_failure() {' "$lib" | head -n 1 | cut -d: -f1)
  if [ -z "$block" ]; then
    echo "expected the failure block to still be a function in $lib; without it this half of the guard reports success for having lost its input" >&2
    return 1
  fi
  # Matched on the call, not on the `|| true` suffix the other guards key on.
  # The suffix is what makes a call gated by the phase; it says nothing about
  # what costs time, and the dominant idiom inside this block is `cozy_diag_read`
  # with no suffix at all. Keyed on the suffix, this check would accept exactly
  # the insertion it exists to refuse. `cozy_diag_phase_*` is out because it is
  # bookkeeping rather than a read, and the trailing space or end-of-line keeps
  # a function definition from matching its own call form.
  ahead_of_red=$(grep -nE '^ *(cozy_capture_[a-z_]+|cozy_report_[a-z_]+|cozy_diag_read|[a-z_]+_diagnose)( |$)' "$lib" \
    | sed -E 's/:[[:space:]]*/:/; s/(:[a-z_]+) .*/\1/' \
    | awk -F: -v b="$block" -v r="$red" '$1 > b && $1 < r { print $2 }')
  case "$(printf '%s\n' $ahead_of_red)" in
    cozy_report_guest_console_wedge) ;;
    '')
      echo "found no collector at all ahead of the failure path's KVM reading; the wedge check belongs there, so this half of the guard checked nothing" >&2
      return 1
      ;;
    *)
      echo "more than the wedge check now runs between the failure block's start and its KVM counter reading, so the red interval is wider than the wait by whatever those collectors cost:" >&2
      printf '%s\n' $ahead_of_red >&2
      return 1
      ;;
  esac
}

@test "both KVM counter readings report a shortfall to the job log, not only to the report" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # The status exists to make a line print, and the reason is not symmetry with
  # the collectors inside the diagnostics block, which are called `|| true` and
  # whose whole record is the report. Both of these readings run outside that
  # block -- one before the node-join wait, one after it on the passing path --
  # and a passing run's report is the artifact nobody opens.
  # docs/agents/e2e-testing.md requires what a passing-path collector could not
  # collect at all to reach the job log. The guest console capture on the
  # passing path is consumed the same way for the same reason, and its own suite
  # pins that shape; this one had nothing pinning it, so rewriting both sites to
  # `|| true` was invisible to every test in the tree.
  #
  # Derived from the source rather than restated, like the call-order checks
  # above: what is pinned is the shape, one `if !` and one warning line per
  # call site, not the wording of either.
  calls=$(grep -c 'if ! cozy_capture_sandbox_kvm_exits [12]; then' "$lib")
  warns=$(grep -c "» WARNING: the sandbox kernel's KVM counters yielded no reading" "$lib")
  if [ "$calls" -ne 2 ]; then
    echo "expected both KVM counter readings outside the diagnostics block to consume the collector's status with 'if !', found $calls" >&2
    return 1
  fi
  if [ "$warns" -ne 2 ]; then
    echo "expected each of those two call sites to put its shortfall in the job log, found $warns warning lines" >&2
    return 1
  fi
}

@test "the declined path leaves no shell error in the diagnostics log" {
  # The chainsaw script that calls this runs under `set -eu`, and the declined
  # path is where a variable assigned only inside the phase gate gets read
  # afterwards. A shell error in this log is not cosmetic: every other line is
  # written to mean something exact, and one that does not teaches the next
  # reader to skim.
  #
  # It runs in a subprocess rather than in this file's shell, and the reason is
  # not `set -u` being off in here -- it is on, `$-` carries `u`. cozytest.sh is
  # `#!/bin/sh` and dot-sources the transformed test file, so on a host where
  # /bin/sh is bash 3.2 (macOS) the body runs under semantics where `local x`
  # with no assignment yields an empty-but-SET variable. `set -u` then has
  # nothing to fire on, and an assertion written in here would pass against the
  # broken code.
  #
  # Which is also why the shell is probed before the assertion is trusted: on
  # such a host this check cannot show the defect at all, and it says so instead
  # of reporting a pass it did not earn.
  if bash -c 'set -u; f() { local a; printf "%s" "${a}"; }; f' >/dev/null 2>&1; then
    echo "not checked: this bash reads an unset local as empty, so set -u cannot surface the defect here" >&2
    return 0
  fi
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  bash -c '
    set -eu
    . hack/e2e-chainsaw/_lib/run-kubernetes.sh
    cozy_report_guest_console_wedge() { :; }
    cozy_capture_tenant_serial_console() { :; }
    cozy_capture_tenant_talos() { :; }
    talos_image_cache_diagnose() { :; }
    cozy_capture_tenant_worker_cpu_throttle() { :; }
    cozy_capture_tenant_worker_network_counters() { :; }
    cozy_capture_sandbox_kvm_exits() { :; }
    cozy_capture_runner_kernel_cpu_time() { :; }
    cozy_capture_sandbox_qemu_thread_cpu() { :; }
    cozy_capture_runner_canary() { :; }
    cozy_capture_tenant_worker_block_io() { :; }
    ghcr_mirror_diagnose() { :; }
    kubectl() { :; }
    COZY_DIAG_PHASE_BUDGET=0
    cozy_report_node_join_failure test-latest-version
  ' >"$tmp/out" 2>&1 || true

  if grep -q 'unbound variable' "$tmp/out"; then
    echo "FAIL: the declined path put a shell error in the diagnostics log:" >&2
    grep 'unbound variable' "$tmp/out" >&2
    false
  fi
  # Not vacuous: the phase really did decline, so the run above walked the path
  # the assertion is about rather than returning early somewhere else.
  assert_file_contains 'not collected' "$tmp/out"
  rm -rf "$tmp"
}

@test "the phase budget leaves the snapshot its time after the phase overshoots and the comments quote the same number" {
  # This is the inequality the budget is derived from, held so the next collector
  # added cannot break it quietly:
  #
  #   budget + largest collector cost + snapshot <= op - bringup
  #
  # Two of its terms are literals below rather than read from source, and each is a
  # literal for a reason worth knowing before this guard is trusted.
  #
  # `bringup` is what the chainsaw comments give for reaching the failure. That is an
  # observed figure and not a ceiling, deliberately: the bringup's own waits already
  # exceed the whole op on ceilings, so a guard written against those would assert a
  # worst case nobody can state. The pre-wait halves of the readings that bracket
  # the node-join wait spend inside it -- the canary's first sample alone is bounded
  # at two arms of (canary + grace) -- so a collector added before the wait moves
  # the real figure while this literal sits still.
  #
  # `largest` is the heaviest collector's cost at the pool's MINIMUM size, so it is a
  # floor rather than a ceiling. It is here because admission gates when a collector
  # may START: one let in a moment early still runs its whole cost, and a guard that
  # drops that term accepts a budget under which the phase finishes past the window.
  #
  # So this catches a budget raised past what today's collectors leave room for, and
  # nothing else. It does not cover the guest-Talos walk growing with the pool, which
  # carries no cap; nor the collector gated last, whose image-cache re-probe has no
  # wall-clock bound at all; nor a bounded read gated ahead of the console outside
  # the CAPTURE-style call form this walk enumerates, as the LINSTOR resource state
  # read is, whose cost is therefore not summed here; nor a new collector heavier
  # than the literal, since nothing makes one move it; nor the pre-wait halves of the four
  # readings that bracket the wait, which spend inside the bringup term this literal fixes
  # rather than inside the budget, and which the canary's first sample alone can carry to
  # two arms of (canary + grace). Those are the residuals, and the first three exist today
  # rather than hypothetically. All are tracked in cozystack/cozystack#3666.
  bringup=1500
  # 620 was this figure while the guest-Talos walk ran two commands per worker.
  # It now runs four: the service list and the link table were added at a 10s
  # bound with the usual 5s kill grace, which is 2 x 2 x 15 = 60 more seconds
  # across the two-worker minimum pool. Derived as a delta on the previous
  # number rather than recomputed from the walk, deliberately: a fresh
  # enumeration that missed a step would LOWER this term and loosen the guard
  # while looking like it had tightened it, and the delta is the part this
  # change is answerable for.
  largest=680
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  # Read from the named default rather than from the `:-` expansion: the defaults are
  # declared once as constants, so that is where the number lives now.
  budget=$(grep -oE '^COZY_DIAG_PHASE_BUDGET_DEFAULT=[0-9]+' "$lib" | head -n 1 | sed -E 's/.*=//')
  if [ -z "$budget" ]; then
    echo "expected a default for COZY_DIAG_PHASE_BUDGET in $lib" >&2
    exit 1
  fi
  # The snapshot's own wall-clock bound, read from the source rather than restated,
  # since a budget that leaves room for a number the collector no longer uses
  # leaves room for nothing.
  snapshot=$(grep -oE 'timeout -k 30 [0-9]+ crust-gather' "$lib" | head -n 1 \
    | sed -E 's/.*-k 30 ([0-9]+).*/\1/')
  # Checked like `budget` above, and for a sharper reason: an empty match makes
  # $((snapshot + 30)) evaluate to 30, and the inequality then passes on a term
  # 360 seconds too small -- a guard that reports success because it lost its input.
  if [ -z "$snapshot" ]; then
    echo "expected to read the tenant snapshot's own bound from $lib" >&2
    exit 1
  fi
  snapshot=$((snapshot + 30))
  # 50m is what both suites give the op, pinned by its own guard in
  # hack/run-kubernetes-talos-diagnostics_test.bats.
  window=$((3000 - bringup))
  if [ "$((budget + largest + snapshot))" -gt "$window" ]; then
    echo "phase budget ${budget}s + largest collector ${largest}s + snapshot ${snapshot}s exceeds the ${window}s left after ${bringup}s of bringup" >&2
    echo "the phase can then end past the point where the snapshot still fits, which is what the budget exists to prevent" >&2
    exit 1
  fi
  # And the comment a reader checks the ceiling against has to carry the same
  # number the code uses; this is the pair that has to stay in step.
  minutes=$((budget / 60))
  for f in hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml \
    hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml; do
    if ! grep -q "own ${minutes}m wall-clock budget" "$f"; then
      echo "expected $f to state the phase budget as ${minutes}m" >&2
      exit 1
    fi
  done
}

@test "the block returns zero even when every read fails so the caller keeps its exit" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stub_collectors
  tmp=$(mktemp -d)
  use_temp_report_dir "$tmp"
  kubectl_calls="$tmp/kubectl.calls"
  unbounded_calls="$tmp/unbounded.calls"
  timeout_calls="$tmp/timeout.calls"
  # Every kubectl invocation carries a dash somewhere, so this refuses all of them.
  kubectl_fail_match='-'
  kubectl_fail_rc=1

  rc=0
  ( set +x; cozy_report_node_join_failure test-latest-version ) >"$tmp/out" 2>&1 || rc=$?

  # The caller's `exit 1` is what fails the suite and what triggers the tenant
  # snapshot. A non-zero return here replaces it under `set -e`, so a collector
  # that could not read anything would decide the suite's exit status.
  [ "$rc" -eq 0 ]
  rm -rf "$tmp"
}
