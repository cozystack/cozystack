#!/usr/bin/env bats
# Successful-node-join timing stays in the normal job log; these mocks keep its
# resource/event rendering cluster-free under hack/cozytest.sh.

kubectl() {
  case "$*" in
    *"get events.events.k8s.io"*)
      printf '%b\n' \
        '2026-08-27T13:50:07Z\tDataVolume\tkubernetes-test-latest-version-md0-a-worker-disk-system\tImportInProgress\tImport in progress' \
        '2026-08-27T13:51:02Z\tDataVolume\tkubernetes-test-latest-version-md0-a-worker-disk-system\tCompleted\tImport Complete' \
        '2026-08-27T13:51:06Z\tPod\tvirt-launcher-kubernetes-test-latest-version-md0-a-worker-abc\tPulled\tSuccessfully pulled image in 3.355s' \
        '2026-08-27T13:51:54Z\tMachine\tkubernetes-test-latest-version-md0-a-worker\tSuccessfulSetNodeRef\tkubernetes-test-latest-version-md0-a-worker'
      ;;
    *"get nodes"*)
      printf '%b\n' 'worker-a\tcreated=2026-08-27T13:51:45Z\tReady=2026-08-27T13:52:03Z'
      ;;
    *"logs deploy/talos-image-cache"*)
      printf '%s\n' '2026-08-27T13:51:01Z transfer-finished status=complete path=/image/openstack-amd64.raw.xz range=0-123/124 requested=124 sent=124 duration=0.432s'
      ;;
    *)
      return 1
      ;;
  esac
}

timeout() {
  shift 3
  "$@"
}

assert_contains() {
  case "$1" in
    *"$2"*) return 0 ;;
  esac
  echo "output did not contain: $2" >&2
  return 1
}

@test "successful join report separates wait, lifecycle and image transfer timing" {
  # shellcheck source=/dev/null
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh

  output=$(cozy_report_node_join_timing \
    test-latest-version tenant-kubeconfig 100 140 345 2026-08-27T13:48:00Z)

  assert_contains "$output" 'worker-pool request -> Ready wait: 40s'
  assert_contains "$output" 'two-node Ready wait: 205s'
  assert_contains "$output" 'worker-pool request -> two nodes Ready: 245s'
  assert_contains "$output" $'DataVolume\tkubernetes-test-latest-version-md0-a-worker-disk-system\tImportInProgress'
  assert_contains "$output" 'Successfully pulled image in 3.355s'
  assert_contains "$output" $'worker-a\tcreated=2026-08-27T13:51:45Z\tReady=2026-08-27T13:52:03Z'
  assert_contains "$output" 'duration=0.432s'
}

@test "worker timing starts immediately before the KubernetesNodes request" {
  lib=hack/e2e-chainsaw/_lib/run-kubernetes.sh
  stamp_line=$(grep -n '_worker_pool_requested_at=$(date +%s)' "$lib" | cut -d: -f1)
  nodes_line=$(grep -n '^kind: KubernetesNodes$' "$lib" | cut -d: -f1)

  [ -n "$stamp_line" ]
  [ -n "$nodes_line" ]
  [ "$stamp_line" -lt "$nodes_line" ]
  [ "$(( nodes_line - stamp_line ))" -le 5 ]
}

@test "guest console summary prices direct GHCR kubelet image work" {
  # shellcheck source=/dev/null
  . hack/e2e-chainsaw/_lib/run-kubernetes.sh
  fixture="_out/tmp/guest-console-kubelet-$$.log"
  mkdir -p "${fixture%/*}"
  printf '%s\n' \
    '[   12.037680] [talos] pulling image ghcr.io/siderolabs/kubelet:v1.35.6: starting...' \
    '[   15.793093] [talos] service[kubelet](Preparing): Creating service runner' \
    '[   17.837505] [talos] service[kubelet](Running): Health check successful' \
    >"$fixture"

  output=$(_cozy_guest_kubelet_image_timing worker-a "$fixture")
  rm -f "$fixture"

  assert_contains "$output" 'kubelet image ghcr.io/siderolabs/kubelet:v1.35.6'
  assert_contains "$output" 'fetch+unpack 3.755s (guest 12.038s -> 15.793s)'
  assert_contains "$output" 'kubelet healthy at guest 17.838s'
  assert_contains "$output" 'source=ghcr.io (no e2e registry mirror)'
}
