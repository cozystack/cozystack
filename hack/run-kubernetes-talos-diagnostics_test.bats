#!/usr/bin/env bats
# Regression coverage for tenant-worker Talos diagnostics in
# hack/e2e-chainsaw/_lib/run-kubernetes.sh. cozytest.sh ends an @test block at
# the first bare closing brace, so command mocks stay at top level.
#
# Scratch directories are removed at the end of the test body, never from a
# `trap ... EXIT`. Such a trap replaces the one the bats binary installs for its
# own bookkeeping, and a test that then fails prints no TAP line at all -- not
# `not ok`, nothing -- so a reader grepping the output for failures sees a green
# suite while the test silently did not finish. Both runners set -e, so on
# failure the cleanup below is unreachable and the directory survives for
# inspection, which is what a failed test wants anyway. See
# docs/agents/e2e-testing.md and hack/bats-no-exit-trap.bats.

kubectl_calls=/dev/null
kubectl_manifest=/dev/null
kubectl_exec_rc=0
kubectl_vmi_json=
talosctl_calls=/dev/null
timeout_calls=/dev/null
timeout_rc=0
timeout_fail_node_ip=
# Per-read failure, which the two knobs above cannot express: one is global and
# the other selects a whole node. The marker below is decided by how many of a
# worker's four reads answered, so every interesting case is a mix -- and with
# only all-or-nothing knobs the boundary between them cannot be reached at all,
# which is how it went untested while a test named for it passed.
#
# Space-separated tokens matched against the composed command; each of the four
# reads carries one the others do not (dmesg, kubelet, links, services).
timeout_fail_commands=

kubectl() {
  printf '%s\n' "$*" >>"${kubectl_calls}"

  if [ "${1:-}" = apply ]; then
    cat >"${kubectl_manifest}"
    return 0
  fi

  if [ "${1:-}" = -n ] && [ "${2:-}" = tenant-test ] \
    && [ "${3:-}" = get ] && [ "${4:-}" = secret ]; then
    if [ "${7:-}" = name ]; then
      printf 'secret/%s\n' "${5}"
      return 0
    fi
    case "${7:-}" in
      *tls.key*) printf '%s\n' 'mock-client-key' ;;
      *) printf '%s\n' 'mock-certificate' ;;
    esac
    return 0
  fi

  if [ "${1:-}" = -n ] && [ "${2:-}" = tenant-test ] \
    && [ "${3:-}" = get ] \
    && [ "${4:-}" = virtualmachineinstances.kubevirt.io ] \
    && [ "${8:-}" = json ] && [ -n "${kubectl_vmi_json}" ]; then
    cat "${kubectl_vmi_json}"
    return 0
  fi

  if [ "${3:-}" = exec ] && [ "${4:-}" = deploy/linstor-controller ]; then
    printf '%s\n' '104857600:satellite-a'
    return 0
  fi

  if [ "${3:-}" = exec ]; then
    printf 'mock capture for %s\n' "$*"
    return "${kubectl_exec_rc}"
  fi

  return 0
}

talosctl() {
  printf '%s\n' "$*" >>"${talosctl_calls}"
  if [ "${1:-}" = --talosconfig ] && [ "${3:-}" = config ] \
    && [ "${4:-}" = add ]; then
    printf '%s\n' 'mock-talosconfig' >"${2}"
  fi
}

timeout() {
  local command_rc=0
  printf '%s\n' "$*" >>"${timeout_calls}"
  [ "${1:-}" = -k ] || return 97
  shift 3
  "$@" || command_rc=$?
  if [ -n "${timeout_fail_node_ip}" ]; then
    case " $* " in
      *" -e ${timeout_fail_node_ip} "*) return 124 ;;
    esac
  fi
  local _tok
  for _tok in ${timeout_fail_commands}; do
    case " $* " in
      *" ${_tok}"*) return 1 ;;
    esac
  done
  [ "${timeout_rc}" -eq 0 ] || return "${timeout_rc}"
  return "${command_rc}"
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

assert_file_lacks_pattern() {
  local pattern="$1"
  local file="$2"

  if awk -v pattern="${pattern}" '$0 ~ pattern { found = 1 } END { exit found ? 0 : 1 }' "${file}"; then
    printf 'expected %s not to match: %s\n' "${file}" "${pattern}" >&2
    return 1
  fi
}

@test "worker VMI rows retain names whose default interface has no IP" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  rows=$(printf '%s\n' '{"items":[{"metadata":{"name":"worker-a"},"status":{"interfaces":[{"name":"default","ipAddress":"10.244.1.92"}]}},{"metadata":{"name":"worker-b"},"status":{"interfaces":[{"name":"other","ipAddress":"192.0.2.1"}]}}]}' | cozy_tenant_worker_vmi_rows)
  [ "${rows}" = "$(printf 'worker-a|10.244.1.92\nworker-b|')" ]
}

@test "reader Certificate is short-lived and never embeds credential data" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  kubectl_manifest="$tmp/certificate.yaml"

  cozy_apply_tenant_talos_reader_certificate test-latest-version

  assert_file_contains 'name: kubernetes-test-latest-version-e2e-talos-reader' "$kubectl_manifest"
  assert_file_contains 'duration: 1h' "$kubectl_manifest"
  assert_file_contains 'commonName: kubernetes-test-latest-version-e2e-talos-reader' "$kubectl_manifest"
  assert_file_contains '- os:reader' "$kubectl_manifest"
  assert_file_contains 'name: kubernetes-test-latest-version-talos-ca' "$kubectl_manifest"
  assert_file_contains 'cozystack-e2e.io/tenant-talos-diagnostics: "test-latest-version"' "$kubectl_manifest"
  assert_file_lacks_pattern '^[[:space:]]*(tls[.]crt|tls[.]key|data):' "$kubectl_manifest"
  rm -rf "$tmp"
}

@test "talosconfig uses the tenant CA and the issued reader key" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  kubectl_manifest="$tmp/certificate.yaml"
  talosctl_calls="$tmp/talosctl.calls"

  cozy_prepare_tenant_talosconfig test-latest-version "$tmp"

  [ "$(sed -n '1p' "$kubectl_calls")" = '-n tenant-test delete certificate kubernetes-test-latest-version-e2e-talos-reader --ignore-not-found --wait=true --timeout=10s' ]
  [ "$(sed -n '2p' "$kubectl_calls")" = '-n tenant-test delete secret kubernetes-test-latest-version-e2e-talos-reader --ignore-not-found --wait=true --timeout=10s' ]
  assert_file_contains 'wait -n tenant-test certificate kubernetes-test-latest-version-e2e-talos-reader --for=condition=Ready --timeout=30s' "$kubectl_calls"
  assert_file_contains '-n tenant-test get secret kubernetes-test-latest-version-talos-ca -o go-template={{index .data "tls.crt" | base64decode}}' "$kubectl_calls"
  assert_file_contains '-n tenant-test get secret kubernetes-test-latest-version-e2e-talos-reader -o go-template={{index .data "tls.key" | base64decode}}' "$kubectl_calls"
  assert_file_contains "--talosconfig $tmp/talosconfig config add kubernetes-test-latest-version --ca $tmp/ca.crt --crt $tmp/client.crt --key $tmp/client.key" "$talosctl_calls"
  assert_file_contains "--talosconfig $tmp/talosconfig config context kubernetes-test-latest-version" "$talosctl_calls"
  case "$(LC_ALL=C ls -ld "$tmp/talosconfig")" in
    -rw-------*) ;;
    *) echo "talosconfig permissions are not 0600" >&2; return 1 ;;
  esac
  rm -rf "$tmp"
}

@test "diagnostics Pod has no API token or Secret volume" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  kubectl_manifest="$tmp/pod.yaml"

  cozy_apply_tenant_talos_diagnostics_pod test-latest-version

  assert_file_contains 'automountServiceAccountToken: false' "$kubectl_manifest"
  assert_file_contains 'runAsNonRoot: true' "$kubectl_manifest"
  assert_file_contains 'readOnlyRootFilesystem: true' "$kubectl_manifest"
  assert_file_contains 'drop:' "$kubectl_manifest"
  assert_file_contains '- ALL' "$kubectl_manifest"
  assert_file_contains 'image: docker.io/alpine/k8s:1.36.2@sha256:44ef4942e171939b9c665a4a84beb80e2dcdb9a24330d4651cfdfd2e9deecc47' "$kubectl_manifest"
  assert_file_contains "'docker.io/alpine/k8s:1.36.2@sha256:44ef4942e171939b9c665a4a84beb80e2dcdb9a24330d4651cfdfd2e9deecc47'" hack/e2e-install-cozystack.bats
  assert_file_lacks_pattern 'secret:|secretName:' "$kubectl_manifest"
  rm -rf "$tmp"
}

@test "Kubernetes Chainsaw operations leave room for failure diagnostics" {
  [ "$(yq 'select(.metadata.name == "kubernetes-latest").spec.steps[0].try[0].script.timeout' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml)" = 67m ]
  [ "$(yq 'select(.metadata.name == "kubernetes-previous").spec.steps[0].try[0].script.timeout' hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml)" = 67m ]
}

@test "orchestrator skips credentials and helper Pod when every VMI lacks an IP" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  kubectl_manifest="$tmp/manifest.yaml"
  kubectl_vmi_json="$tmp/vmis.json"
  talosctl_calls="$tmp/talosctl.calls"
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=diagnostic-no-ip
  printf '%s\n' '{"items":[{"metadata":{"name":"worker-a"},"status":{"interfaces":[{"name":"default"}]}},{"metadata":{"name":"worker-b"},"status":{}}]}' >"$kubectl_vmi_json"

  capture_rc=0
  cozy_capture_tenant_talos test-latest-version || capture_rc=$?

  [ "$capture_rc" -eq 1 ]
  assert_file_lacks_pattern '(^|[[:space:]])apply([[:space:]]|$)' "$kubectl_calls"
  assert_file_lacks_pattern 'delete (certificate|secret|pod)|wait (certificate|pod)|exec' "$kubectl_calls"
  [ ! -s "$talosctl_calls" ]
  assert_file_contains 'default VMI interface has no reported IP address' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-a/capture-error.log"
  assert_file_contains 'default VMI interface has no reported IP address' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-b/capture-error.log"
  assert_file_contains 'no tenant worker VMI has a reported IP for Talos diagnostics' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/setup-error.log"
  rm -rf "$tmp"
}

@test "node capture uses the VMI IP for endpoint and node with bounded commands" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

  [ -s "$tmp/node/dmesg.log" ]
  [ -s "$tmp/node/kubelet.log" ]
  [ "$(sed -n '1p' "$kubectl_calls")" = '-n tenant-test exec diagnostics-pod -c diagnostics -- /tmp/talosctl --talosconfig /tmp/talosconfig -e 10.244.1.92 -n 10.244.1.92 dmesg' ]
  [ "$(sed -n '2p' "$kubectl_calls")" = '-n tenant-test exec diagnostics-pod -c diagnostics -- /tmp/talosctl --talosconfig /tmp/talosconfig -e 10.244.1.92 -n 10.244.1.92 logs kubelet --tail=500' ]
  [ "$(sed -n '1p' "$timeout_calls")" = '-k 5 20 kubectl -n tenant-test exec diagnostics-pod -c diagnostics -- /tmp/talosctl --talosconfig /tmp/talosconfig -e 10.244.1.92 -n 10.244.1.92 dmesg' ]
  [ "$(sed -n '2p' "$timeout_calls")" = '-k 5 20 kubectl -n tenant-test exec diagnostics-pod -c diagnostics -- /tmp/talosctl --talosconfig /tmp/talosconfig -e 10.244.1.92 -n 10.244.1.92 logs kubelet --tail=500' ]
  case "$(sed -n '1p' "$kubectl_calls")" in
    *--tail*) echo "dmesg must not receive a --tail flag" >&2; exit 1 ;;
  esac
  assert_file_contains '[capture exit code: 0]' "$tmp/node/dmesg.log"
  assert_file_contains '[capture exit code: 0]' "$tmp/node/kubelet.log"
  rm -rf "$tmp"
}

@test "the service state machine is captured, so a kubelet that never ran is legible" {
  # The log this collector took before was not the whole answer, and on the
  # dominant shape of this failure it is not an answer at all. Talos creates a
  # service's log buffer when its runner opens the log writer, as it starts the
  # process, so a kubelet held in Waiting -- for its volumes, for time sync, or
  # for the container runtime to report healthy -- never reached its runner, has
  # no log to read, and `logs kubelet` fails naming a log never registered.
  # That error is a true statement about the log and says
  # nothing about the service, so a reader gets an unexplained failure where the
  # actual finding is the state and the condition it is waiting on. ServiceList
  # carries both, for every service at once, and the reader role is allowed to
  # call it.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

  [ -s "$tmp/node/services.log" ]
  assert_file_contains ' 10.244.1.92 services' "$kubectl_calls"
  assert_file_contains '[capture exit code: 0]' "$tmp/node/services.log"
  rm -rf "$tmp"
}

@test "the guest link table is captured as yaml, because MTU is not a printed column" {
  # MTU is the value that decides whether the PMTU reading of a stalled transfer
  # is alive: a guest at 1500 over an encapsulated fabric drops exactly the large
  # segments while the small ones pass. LinkStatus carries it in the spec and
  # not in the resource's print columns, so the table form -- which is what the
  # command prints by default -- omits the one field this is read for.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

  # Named .yaml.log, not .yaml, and the suffix is the assertion. Every capture
  # here gets `[capture exit code: N]` appended by the shared helper, so a file
  # promising YAML would not parse as YAML -- and a reader who reaches for yq
  # and gets a parse error learns something about the tool rather than about the
  # cluster. The .log suffix matches every other consumer of that footer.
  [ -s "$tmp/node/links.yaml.log" ]
  [ ! -e "$tmp/node/links.yaml" ]
  assert_file_contains ' 10.244.1.92 get links -o yaml' "$kubectl_calls"
  rm -rf "$tmp"
}

@test "the two added reads carry the tighter bound and the two older ones do not" {
  # The phase budget is what makes this a decision rather than a detail. These
  # two reads sit inside the collector the budget's own guard sizes itself
  # against, so every second added here is a second taken from the collectors
  # gated after it. Both are single tabular RPCs that a reachable apid answers
  # at once, which is not the shape of the two reads beside them: a kernel ring
  # buffer and five hundred lines of service log are streams, and cutting those
  # to ten seconds would lose evidence on exactly the slow-apid worker the
  # capture exists for.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

  while IFS= read -r line; do
    case "$line" in
      *' dmesg') [ "${line#-k 5 20 }" != "$line" ] || { echo "dmesg lost its 20s bound: $line" >&2; exit 1 ;} ;;
      *'logs kubelet --tail=500') [ "${line#-k 5 20 }" != "$line" ] || { echo "kubelet log lost its 20s bound: $line" >&2; exit 1 ;} ;;
      *' services') [ "${line#-k 5 10 }" != "$line" ] || { echo "services must carry the tighter bound: $line" >&2; exit 1 ;} ;;
      *'get links -o yaml') [ "${line#-k 5 10 }" != "$line" ] || { echo "links must carry the tighter bound: $line" >&2; exit 1 ;} ;;
      *) echo "unexpected bounded call: $line" >&2; exit 1 ;;
    esac
  done <"$timeout_calls"
  [ "$(wc -l < "$timeout_calls")" -eq 4 ]
  rm -rf "$tmp"
}

@test "a worker that produced no guest evidence at all says so in its own directory" {
  # The gap this closes is a directory that looks like a worker with nothing to
  # report. When apid refuses the connection every read fails and each log holds
  # its own rpc error plus an exit code, which is accurate per file and adds up
  # to nothing a reader scanning the tree can see: the directory has the same
  # shape as one whose reads returned little. The marker states the outcome for
  # the worker rather than for each read, and it is written only when NOTHING
  # succeeded, so a worker that answered one read out of four is not written up
  # as unreachable.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_rc=1

  rc=0
  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.93 "$tmp/node" || rc=$?

  # A zero status on the all-fail path, which is the ordinary shape of an
  # unreachable worker. Note what this assertion does NOT establish: the call
  # above uses `|| rc=$?`, which suppresses errexit for everything it covers, so
  # a failing statement inside the function would not abort here either way.
  # That the function keeps its caller's walk alive under a live errexit is a
  # property of the `|| true` on the marker write, and it is not observable from
  # this suite -- reaching it needs a bare call in a shell this file does not
  # provide.
  [ "$rc" -eq 0 ]
  assert_file_contains 'no Talos read completed for this worker' "$tmp/node/CAPTURE-FAILED.txt"
  # The per-read logs stay: they carry the reason, and the marker only points.
  assert_file_contains '[capture exit code: 1]' "$tmp/node/dmesg.log"
  rm -rf "$tmp"
}

@test "a worker that answered even one read carries no capture-failed marker" {
  # The positive control for the marker above, and it has to sit at the
  # BOUNDARY rather than at the easy end. The marker's contract is "nothing
  # answered", so the case that distinguishes it from every nearby threshold is
  # one read answering and three failing -- not four answering, which is what
  # this test used to stage and which any `answered < N` would also satisfy.
  # Measured: with the all-succeed staging, changing the condition to
  # `-lt 4` left this file green while a worker that answered three reads of
  # four was written up as having produced no guest evidence at all, which is
  # the false statement the marker exists to prevent.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  # Everything but the service list fails, so exactly one read answers.
  timeout_fail_commands="dmesg kubelet links"

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

  [ ! -f "$tmp/node/CAPTURE-FAILED.txt" ]
  # Not vacuous: the staging really did leave three reads failing and one
  # answering, so the absence above is the boundary rather than an easy case.
  assert_file_contains '[capture exit code: 1]' "$tmp/node/dmesg.log"
  assert_file_contains '[capture exit code: 0]' "$tmp/node/services.log"
  rm -rf "$tmp"
}

@test "every one of the four reads counts toward the tally, not just the last" {
  # The other direction of the same boundary, and it has to walk all four
  # readings rather than restage the one above. The marker is decided by a COUNT
  # over four reads, so a read that stops contributing is invisible in any
  # staging where some other read answers -- it only changes which staging
  # reaches zero. Measured before this test existed: dropping the tally from
  # three of the four reads at once left the whole file green, because the one
  # staging in use happened to keep the fourth.
  #
  # So each read takes a turn as the sole answering one. Dropping any single
  # read's contribution makes its own turn report a worker that answered
  # nothing, while its own log in the same directory holds a clean capture.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  for sole in dmesg kubelet links services; do
    tmp=$(mktemp -d)
    kubectl_calls="$tmp/kubectl.calls"
    timeout_calls="$tmp/timeout.calls"
    timeout_fail_commands=$(printf '%s\n' dmesg kubelet links services | grep -v "^${sole}$" | tr '\n' ' ')

    cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.92 "$tmp/node"

    case "$sole" in
      dmesg) log="dmesg.log" ;;
      kubelet) log="kubelet.log" ;;
      links) log="links.yaml.log" ;;
      services) log="services.log" ;;
    esac
    # Not vacuous: the staging really did leave this read as the only answer.
    assert_file_contains '[capture exit code: 0]' "$tmp/node/$log"
    if [ -f "$tmp/node/CAPTURE-FAILED.txt" ]; then
      echo "FAIL: with $sole the only answering read, the worker was declared to have produced nothing" >&2
      rm -rf "$tmp"
      false
    fi
    rm -rf "$tmp"
  done
}

@test "a timed-out worker remains recorded and does not block the next capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_rc=124

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.93 "$tmp/node"

  [ "$(wc -l < "$kubectl_calls")" -eq 4 ]
  [ "$(wc -l < "$timeout_calls")" -eq 4 ]
  assert_file_contains '[capture exit code: 124]' "$tmp/node/dmesg.log"
  assert_file_contains '[capture exit code: 124]' "$tmp/node/kubelet.log"
  assert_file_contains '[capture exit code: 124]' "$tmp/node/services.log"
  rm -rf "$tmp"
}

@test "orchestrator continues from a timed-out worker to the next VMI" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  origin=$PWD
  kubectl_calls="$tmp/kubectl.calls"
  kubectl_manifest="$tmp/manifest.yaml"
  kubectl_vmi_json="$tmp/vmis.json"
  talosctl_calls="$tmp/talosctl.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_fail_node_ip=10.244.1.93
  COZY_REPORT_DIR="$tmp/report"
  COZY_SNAPSHOT_NAME=diagnostic-smoke
  printf '%s\n' '{"items":[{"metadata":{"name":"worker-no-ip"},"status":{"interfaces":[{"name":"default"}]}},{"metadata":{"name":"worker-a"},"status":{"interfaces":[{"name":"default","ipAddress":"10.244.1.93"}]}},{"metadata":{"name":"worker-b"},"status":{"interfaces":[{"name":"default","ipAddress":"10.244.1.94"}]}}]}' >"$kubectl_vmi_json"
  printf '%s\n' '#!/bin/sh' >"$tmp/talosctl"
  chmod 755 "$tmp/talosctl"
  cd "$tmp"

  cozy_capture_tenant_talos test-latest-version

  [ "$(awk '/ exec kubernetes-test-latest-version-talos-diagnostics / { count++ } END { print count + 0 }' "$kubectl_calls")" -eq 8 ]
  assert_file_contains 'default VMI interface has no reported IP address' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-no-ip/capture-error.log"
  assert_file_contains '[capture exit code: 124]' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-a/dmesg.log"
  assert_file_contains '[capture exit code: 124]' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-a/kubelet.log"
  assert_file_contains '[capture exit code: 0]' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-b/dmesg.log"
  assert_file_contains '[capture exit code: 0]' "$COZY_REPORT_DIR/snapshots/$COZY_SNAPSHOT_NAME/tenant-talos/worker-b/kubelet.log"
  # Back out of the scratch directory before removing it: this test runs the
  # orchestrator from inside it. Not because rm would fail -- BSD, busybox and
  # GNU rm all remove a directory from inside it and return 0 -- but because
  # the runner's own bookkeeping still runs in this subshell after the body
  # returns, and it would be doing so on an unlinked cwd.
  cd "$origin"
  rm -rf "$tmp"
}

@test "cleanup deletes diagnostic Certificate before its Pod and Secret" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"

  cozy_cleanup

  [ "$(sed -n '2p' "$kubectl_calls")" = '-n tenant-test delete certificates.cert-manager.io -l cozystack-e2e.io/tenant-talos-diagnostics --ignore-not-found --wait=true --timeout=30s' ]
  [ "$(sed -n '3p' "$kubectl_calls")" = '-n tenant-test delete pod,secret -l cozystack-e2e.io/tenant-talos-diagnostics --ignore-not-found --wait=false' ]
  rm -rf "$tmp"
}

@test "a worker whose reads were cut short is not told its directory is empty" {
  # The slow-apid worker, and the shape this capture is most often reached on:
  # every read is killed at its bound after writing part of what it fetched, so
  # nothing exits zero and the tally reads nothing-completed -- while the
  # directory holds real evidence. A marker saying nothing was observed would be
  # this collector making the unsupported claim its own status arms exist to
  # prevent, one directory up.
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  # Output first, then the non-zero status: what `timeout` does to a stream it
  # kills, and the reason the mock cannot return before writing.
  timeout_rc=124

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.93 "$tmp/node"

  # Not vacuous: a read really did leave content behind before it was killed.
  assert_file_contains 'mock capture for' "$tmp/node/dmesg.log"
  assert_file_contains '[capture exit code: 124]' "$tmp/node/dmesg.log"
  # The marker still fires -- nothing completed -- but says only that.
  assert_file_contains 'no Talos read completed for this worker' "$tmp/node/CAPTURE-FAILED.txt"
  assert_file_lacks_pattern 'nothing here was observed' "$tmp/node/CAPTURE-FAILED.txt"
  rm -rf "$tmp"
}
