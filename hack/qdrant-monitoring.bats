#!/usr/bin/env bats

load test_helper

setup() {
  strict_setup
  REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)"
  export KUBECONFIG=/dev/null
}

render_monitor() {
  helm template test-qdrant "$REPO_ROOT/packages/apps/qdrant" \
    --namespace tenant-test --set _namespace.host=tenant.example.test "$@" > "$BATS_TEST_TMPDIR/app.yaml"
  yq 'select(.kind == "HelmRelease") | .spec.values' "$BATS_TEST_TMPDIR/app.yaml" > "$BATS_TEST_TMPDIR/values.yaml"
  helm template test-qdrant-system "$REPO_ROOT/packages/system/qdrant" \
    --namespace tenant-test --values "$BATS_TEST_TMPDIR/values.yaml" > "$BATS_TEST_TMPDIR/rendered.yaml"
  local count index
  count=$(yq 'select(.kind == "HelmRelease") | .spec.postRenderers | length' "$BATS_TEST_TMPDIR/app.yaml")
  index=0
  while [ "$index" -lt "$count" ]; do
    INDEX="$index" yq 'select(.kind == "HelmRelease") | .spec.postRenderers[env(INDEX)].kustomize + {
      "apiVersion": "kustomize.config.k8s.io/v1beta1",
      "kind": "Kustomization",
      "resources": ["rendered.yaml"]
    }' "$BATS_TEST_TMPDIR/app.yaml" > "$BATS_TEST_TMPDIR/kustomization.yaml"
    kubectl kustomize "$BATS_TEST_TMPDIR" > "$BATS_TEST_TMPDIR/next.yaml"
    mv "$BATS_TEST_TMPDIR/next.yaml" "$BATS_TEST_TMPDIR/rendered.yaml"
    index=$((index + 1))
  done
  yq 'select(.kind == "ServiceMonitor") | .spec.endpoints[0]' "$BATS_TEST_TMPDIR/rendered.yaml" > "$BATS_TEST_TMPDIR/endpoint.yaml"
  [ "$(yq '.authorization.credentials.name' "$BATS_TEST_TMPDIR/endpoint.yaml")" = test-qdrant-apikey ]
  [ "$(yq '.authorization.credentials.key' "$BATS_TEST_TMPDIR/endpoint.yaml")" = api-key ]
  [ "$(yq '.port' "$BATS_TEST_TMPDIR/endpoint.yaml")" = http ]
}

@test "TLS monitoring uses the issued CA and a certificate SAN" {
  render_monitor --set tls.enabled=true --set _cluster.cluster-domain=cluster.test
  [ "$(yq '.scheme' "$BATS_TEST_TMPDIR/endpoint.yaml")" = https ]
  [ "$(yq '.tlsConfig.ca.secret.name' "$BATS_TEST_TMPDIR/endpoint.yaml")" = test-qdrant-tls ]
  [ "$(yq '.tlsConfig.ca.secret.key' "$BATS_TEST_TMPDIR/endpoint.yaml")" = ca.crt ]
  [ "$(yq '.tlsConfig.insecureSkipVerify // false' "$BATS_TEST_TMPDIR/endpoint.yaml")" = false ]
  server_name="$(yq '.tlsConfig.serverName' "$BATS_TEST_TMPDIR/endpoint.yaml")"
  SERVER_NAME="$server_name" yq --exit-status 'select(.kind == "Certificate" and .metadata.name == "test-qdrant-tls") | .spec.dnsNames | contains([strenv(SERVER_NAME)])' "$BATS_TEST_TMPDIR/app.yaml"
}

@test "external Qdrant enables HTTPS monitoring by default" {
  render_monitor --set external=true
  [ "$(yq '.scheme' "$BATS_TEST_TMPDIR/endpoint.yaml")" = https ]
}

@test "TLS monitoring keeps the image override after the ServiceMonitor patch" {
  render_monitor --set tls.enabled=true --set-json '_cluster.image-overrides=[{"name":"docker.io/qdrant/qdrant","newName":"mirror.example/qdrant"}]'
  [ "$(yq '.scheme' "$BATS_TEST_TMPDIR/endpoint.yaml")" = https ]
  yq --exit-status 'select(.kind == "StatefulSet") | .spec.template.spec.containers[] | select(.name == "qdrant") | .image | test("^mirror[.]example/qdrant:")' "$BATS_TEST_TMPDIR/rendered.yaml"
}

@test "disabled TLS keeps HTTP monitoring without TLS configuration" {
  render_monitor --set external=true --set tls.enabled=false
  [ "$(yq '.scheme' "$BATS_TEST_TMPDIR/endpoint.yaml")" = http ]
  [ "$(yq 'has("tlsConfig")' "$BATS_TEST_TMPDIR/endpoint.yaml")" = false ]
}
