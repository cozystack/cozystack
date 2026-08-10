#!/usr/bin/env bats
# EXIT-TRAP DEBT: 1 -- see hack/bats-no-exit-trap.bats.
# This one is NOT debt and must not be converted. The trap sits inside an
# explicit subshell and kills a backgrounded `kubectl proxy`; a subshell trap
# does not replace the one the bats binary installs, so a failure there still
# prints its `not ok`. Moving the kill to the end of the body would leak the
# proxy on failure -- it holds a fixed port, so the next run wedges. The count
# is declared anyway because the ratchet is exact in both directions: it fails
# if a real test-level trap is added here, and it fails if this one is removed.
# -----------------------------------------------------------------------------
# Test OpenAPI endpoints in a Kubernetes cluster
# -----------------------------------------------------------------------------

@test "Test OpenAPI v2 endpoint" {
  kubectl get -v7 --raw '/openapi/v2?timeout=32s' > /dev/null
}

@test "Test OpenAPI v3 endpoint" {
  kubectl get -v7 --raw '/openapi/v3/apis/apps.cozystack.io/v1alpha1' > /dev/null
  kubectl get -v7 --raw '/openapi/v3/apis/core.cozystack.io/v1alpha1' > /dev/null
}

@test "Test OpenAPI v2 endpoint (protobuf)" {
  (
    kubectl proxy --port=21234 &
    proxy_pid=$!
    trap "kill $proxy_pid" EXIT
    # Wait for the proxy to actually be listening rather than guessing with
    # a fixed sleep. nc -z is non-destructive and exits 0 the moment the
    # listener accepts a connection.
    timeout 10 sh -ec 'until nc -z localhost 21234; do sleep 0.1; done'
    curl -sS --fail 'http://localhost:21234/openapi/v2?timeout=32s' -H 'Accept: application/com.github.proto-openapi.spec.v2@v1.0+protobuf' > /dev/null
  )
}

@test "Test kinds" {
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/tenants | jq -r '.kind')
  if [ "$val" != "TenantList" ]; then
    echo "Expected kind to be TenantList, got $val"
    exit 1
  fi
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/tenants | jq -r '.items[0].kind')
  if [ "$val" != "Tenant" ]; then
    echo "Expected kind to be Tenant, got $val"
    exit 1
  fi
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/ingresses | jq -r '.kind')
  if [ "$val" != "IngressList" ]; then
    echo "Expected kind to be IngressList, got $val"
    exit 1
  fi
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/ingresses | jq -r '.items[0].kind')
  if [ "$val" != "Ingress" ]; then
    echo "Expected kind to be Ingress, got $val"
    exit 1
  fi
}

@test "Create and delete namespace" {
  kubectl create ns cozy-test-create-and-delete-namespace --dry-run=client -o yaml | kubectl apply -f -
  if ! kubectl delete ns cozy-test-create-and-delete-namespace; then
    echo "Failed to delete namespace"
    kubectl describe ns cozy-test-create-and-delete-namespace
    exit 1
  fi
}
