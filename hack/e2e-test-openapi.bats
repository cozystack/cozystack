#!/usr/bin/env bats
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
    kubectl proxy --port=21234 & sleep 0.5
    trap "kill $!" EXIT
    curl -sS --fail 'http://localhost:21234/openapi/v2?timeout=32s' -H 'Accept: application/com.github.proto-openapi.spec.v2@v1.0+protobuf' > /dev/null
  )
}

@test "Test kinds" {
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/tenants | jq -r '.items[0].kind')
  if [ "$val" != "Tenant" ]; then
    echo "Expected kind to be Tenant, got $val"
    exit 1
  fi
  val=$(kubectl get --raw /apis/apps.cozystack.io/v1alpha1/ingresses | jq -r '.items[0].kind')
  if [ "$val" != "Ingress" ]; then
    echo "Expected kind to be Ingress, got $val"
    exit 1
  fi
}
