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
  [ "$(yq 'select(.metadata.name == "kubernetes-latest").spec.steps[0].try[0].script.timeout' hack/e2e-chainsaw/kubernetes-latest/chainsaw-test.yaml)" = 50m ]
  [ "$(yq 'select(.metadata.name == "kubernetes-previous").spec.steps[0].try[0].script.timeout' hack/e2e-chainsaw/kubernetes-previous/chainsaw-test.yaml)" = 50m ]
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

@test "a timed-out worker remains recorded and does not block the next capture" {
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  tmp=$(mktemp -d)
  kubectl_calls="$tmp/kubectl.calls"
  timeout_calls="$tmp/timeout.calls"
  timeout_rc=124

  cozy_capture_tenant_talos_node diagnostics-pod 10.244.1.93 "$tmp/node"

  [ "$(wc -l < "$kubectl_calls")" -eq 2 ]
  [ "$(wc -l < "$timeout_calls")" -eq 2 ]
  assert_file_contains '[capture exit code: 124]' "$tmp/node/dmesg.log"
  assert_file_contains '[capture exit code: 124]' "$tmp/node/kubelet.log"
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

  [ "$(awk '/ exec kubernetes-test-latest-version-talos-diagnostics / { count++ } END { print count + 0 }' "$kubectl_calls")" -eq 4 ]
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
