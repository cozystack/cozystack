#!/usr/bin/env bats

# Phase-1 OIDC System-mode e2e — render-side only.
#
# Pairs with kubernetes-oidc-customconfig.bats (BYO issuer path).
#
# Does NOT spin up a full Kamaji control plane: that path is covered by
# the existing kubernetes-{latest,previous}.bats and would add another
# ~25 min Kamaji bringup for a feature whose end-to-end OIDC browser
# flow is explicitly out of scope (see docs/oidc-tenant.md → Phase 1).
#
# Deferred to a follow-up integration suite, on top of the existing
# heavy harness in kubernetes-latest.bats:
#   - CRBs land inside the tenant cluster (one per users[] entry).
#   - view user is read-only, admin user is cluster-admin.
#   - Pre-delete cleanup removes the CRBs.
#   - kubectl oidc-login browser flow against a real Keycloak realm.
#
# What is exercised here on a live cluster: that the new `spec.oidc.*`
# fields are admitted by the Kubernetes CRD; that cozystack-api accepts
# the updated schema; that the HelmRelease renders the OIDC templates;
# and that the resulting KeycloakClient / KeycloakClientScope /
# AuthenticationConfiguration Secret / KamajiControlPlane carry the
# expected per-cluster shape.

# cozytest.sh (the e2e runner) is not real bats — it never invokes
# setup()/teardown(). Cleanup belongs in cozy_cleanup(), which runs at
# suite exit and on the first failing test. Per-test isolation is done
# inline at the top of each @test.
TEST_NAME="oidc-system"

cleanup_kr() {
  kubectl -n tenant-test delete kubernetes.apps.cozystack.io "${TEST_NAME}" \
    --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kubernetes.apps.cozystack.io "${TEST_NAME}" \
    --for=delete --timeout=2m 2>/dev/null || true
}

cozy_cleanup() { cleanup_kr; }

@test "Kubernetes CR accepts spec.oidc.mode=System with users[]" {
  cleanup_kr
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${TEST_NAME}
  namespace: tenant-test
spec:
  addons:
    ingressNginx:
      enabled: false
      hosts: []
      valuesOverride: {}
  controlPlane:
    apiServer:
      resources: {}
      resourcesPreset: small
    replicas: 1
  nodeGroups:
    md0:
      diskSize: 20Gi
      gpus: []
      instanceType: u1.micro
      maxReplicas: 1
      minReplicas: 0
      resources: {}
      roles:
      - worker
  storageClass: replicated
  version: v1.35
  oidc:
    mode: System
    users:
      - email: e2e-admin@example.test
        role: admin
      - email: e2e-viewer@example.test
        role: view
EOF

  # Wait for cozystack-api to materialise the HelmRelease.
  timeout 60 sh -ec 'until kubectl -n tenant-test get hr "kubernetes-'"${TEST_NAME}"'" >/dev/null 2>&1; do sleep 2; done'

  # Wait for the chart to render and apply objects. We do not wait on
  # the HR Ready condition here — that requires the Kamaji bringup,
  # which the cheap render-side test deliberately avoids.
  timeout 180 sh -ec 'until kubectl -n tenant-test get kamajicontrolplane "kubernetes-'"${TEST_NAME}"'" >/dev/null 2>&1; do sleep 5; done'
}

@test "KamajiControlPlane carries --authentication-config and the chart-owned OIDC volume" {
  KCP="kubernetes-${TEST_NAME}"

  # --authentication-config is appended to spec.apiServer.extraArgs.
  args=$(kubectl -n tenant-test get kamajicontrolplane "${KCP}" -o jsonpath='{.spec.apiServer.extraArgs}')
  echo "extraArgs: ${args}"
  echo "${args}" | grep -q -- "--authentication-config=/etc/kubernetes/authentication-config/config.yaml"

  # The chart-owned mount lands on spec.apiServer.extraVolumeMounts.
  mounts=$(kubectl -n tenant-test get kamajicontrolplane "${KCP}" -o jsonpath='{.spec.apiServer.extraVolumeMounts[*].name}')
  echo "extraVolumeMounts: ${mounts}"
  echo "${mounts}" | grep -qw "authentication-config"

  # The chart-owned volume lands on spec.deployment.extraVolumes.
  vols=$(kubectl -n tenant-test get kamajicontrolplane "${KCP}" -o jsonpath='{.spec.deployment.extraVolumes[*].name}')
  echo "extraVolumes: ${vols}"
  echo "${vols}" | grep -qw "authentication-config"
}

@test "AuthenticationConfiguration Secret carries the cozy realm issuer + per-cluster audience" {
  SEC="kubernetes-${TEST_NAME}-oidc-authn-config"
  # Existence backstop before `kubectl wait` — the HR renders the Secret
  # asynchronously and `kubectl wait` on a non-existent object errors out
  # immediately with NotFound (`--for=jsonpath` only starts polling once
  # the object appears).
  timeout 60 sh -ec 'until kubectl -n tenant-test get secret "'"${SEC}"'" >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test wait secret "${SEC}" --for=jsonpath='{.metadata.name}'="${SEC}" --timeout=60s
  body=$(kubectl -n tenant-test get secret "${SEC}" -o jsonpath='{.data.config\.yaml}' | base64 -d)
  echo "${body}" | grep -qE 'url: https://keycloak\.[^/]+/realms/cozy'
  echo "${body}" | grep -qF -- "- tenant-test-kubernetes-${TEST_NAME}"
}

@test "Per-cluster KeycloakClient + KeycloakClientScope land in the cozy realm" {
  # The Keycloak operator API group is gated on platform OIDC being on; skip
  # the assertion gracefully when the runner does not have it installed.
  if ! kubectl api-resources --api-group=v1.edp.epam.com >/dev/null 2>&1; then
    skip "EDP Keycloak operator CRDs not present on this cluster"
  fi

  CID="tenant-test-kubernetes-${TEST_NAME}"
  timeout 60 sh -ec 'until kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "'"${CID}"'" >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${CID}" \
    -o jsonpath='{.spec.public}' | grep -q '^true$'
  kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${CID}-audience" \
    -o jsonpath='{.spec.protocolMappers[0].protocolMapper}' | grep -q '^oidc-audience-mapper$'
}
