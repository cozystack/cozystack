#!/usr/bin/env bats

@test "Create OpenBAO (standalone)" {
  name='test'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: OpenBAO
metadata:
  name: $name
  namespace: tenant-test
spec:
  replicas: 1
  size: 10Gi
  storageClass: ""
  resourcesPreset: "small"
  resources: {}
  external: false
  ui: true
EOF
  sleep 5
  kubectl -n tenant-test wait hr openbao-$name --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait hr openbao-$name-system --timeout=120s --for=condition=ready

  # Wait for container to be started (pod Running does not guarantee container is ready for exec on slow CI)
  if ! timeout 120 sh -ec "until kubectl -n tenant-test get pod openbao-$name-0 --output jsonpath='{.status.containerStatuses[0].started}' 2>/dev/null | grep -q true; do sleep 5; done"; then
    echo "=== DEBUG: Container did not start in time ===" >&2
    kubectl -n tenant-test describe pod openbao-$name-0 >&2 || true
    kubectl -n tenant-test logs openbao-$name-0 --previous >&2 || true
    kubectl -n tenant-test logs openbao-$name-0 >&2 || true
    return 1
  fi

  # Wait for OpenBAO API to accept connections
  # bao status exit codes: 0 = unsealed, 1 = error/not ready, 2 = sealed but responsive
  if ! timeout 60 sh -ec "until kubectl -n tenant-test exec openbao-$name-0 -- bao status >/dev/null 2>&1; rc=\$?; test \$rc -eq 0 -o \$rc -eq 2; do sleep 3; done"; then
    echo "=== DEBUG: OpenBAO API did not become responsive ===" >&2
    kubectl -n tenant-test describe pod openbao-$name-0 >&2 || true
    kubectl -n tenant-test logs openbao-$name-0 --previous >&2 || true
    kubectl -n tenant-test logs openbao-$name-0 >&2 || true
    return 1
  fi

  # Initialize OpenBAO (single key share for testing simplicity)
  init_output=$(kubectl -n tenant-test exec openbao-$name-0 -- bao operator init -key-shares=1 -key-threshold=1 -format=json)
  unseal_key=$(echo "$init_output" | jq -r '.unseal_keys_b64[0]')
  if [ -z "$unseal_key" ] || [ "$unseal_key" = "null" ]; then
    echo "Failed to extract unseal key. Init output: $init_output" >&2
    return 1
  fi

  # Unseal OpenBAO
  kubectl -n tenant-test exec openbao-$name-0 -- bao operator unseal "$unseal_key"

  # Now wait for pod to become ready (readiness probe checks seal status)
  kubectl -n tenant-test wait sts openbao-$name --timeout=90s --for=jsonpath='{.status.readyReplicas}'=1
  kubectl -n tenant-test wait pvc data-openbao-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test delete openbao.apps.cozystack.io $name
  kubectl -n tenant-test delete pvc data-openbao-$name-0 --ignore-not-found
}
