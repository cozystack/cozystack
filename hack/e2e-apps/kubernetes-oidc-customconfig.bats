#!/usr/bin/env bats

# Phase-1 OIDC CustomConfig selector e2e — render-side only.
#
# Does NOT spin up a full Kamaji control plane (the existing
# kubernetes-{latest,previous}.bats already cover that path) and does
# NOT exercise the browser-flow `kubectl oidc-login` end-to-end
# (explicitly out of scope per the design proposal).
#
# What is exercised on a live cluster: that the new
# `spec.oidc.mode: CustomConfig` field is admitted; that cozystack-api
# accepts the updated schema; that the HelmRelease renders; that the
# tenant-supplied AuthenticationConfiguration Secret is wired onto
# the KamajiControlPlane; and — crucially — that NO Keycloak objects
# are created in the `cozy` realm when the tenant brings their own
# issuer.

# cozytest.sh (the e2e runner) is not real bats — it never invokes
# setup()/teardown(). Cleanup belongs in cozy_cleanup(), which runs at
# suite exit and on the first failing test. Per-test isolation is done
# inline at the top of each @test.
TEST_NAME="oidc-byo"

cleanup_kr() {
  kubectl -n tenant-test delete kubernetes.apps.cozystack.io "${TEST_NAME}" \
    --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait kubernetes.apps.cozystack.io "${TEST_NAME}" \
    --for=delete --timeout=2m 2>/dev/null || true
}

cozy_cleanup() { cleanup_kr; }

@test "Kubernetes CR accepts spec.oidc.mode=CustomConfig with inline config" {
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
  # nodeGroups intentionally omitted — this is a render-side test that
  # only asserts on the KamajiControlPlane + the tenant-supplied issuer
  # AuthenticationConfiguration Secret. With the schema default `{}`,
  # the chart helper emits the default `md0` group with
  # `minReplicas: 0` — exactly one MachineDeployment renders
  # (API-surface completeness) but the chart no longer manages
  # `spec.replicas`, so CAPI's defaulting webhook seeds it to 0
  # from the autoscaler min-size annotation. Result: zero
  # KubevirtMachines, zero worker DataVolumes, no DRBD churn between
  # CI runs (see #3231).
  version: v1.35
  oidc:
    mode: CustomConfig
    customConfig:
      config: |
        apiVersion: apiserver.config.k8s.io/v1beta1
        kind: AuthenticationConfiguration
        jwt:
        - issuer:
            url: https://idp.byo.example.test
            audiences:
            - cozystack-byo-${TEST_NAME}
          claimMappings:
            username:
              claim: preferred_username
              prefix: ""
            groups:
              claim: groups
              prefix: ""
    users:
      - email: byo-admin@example.test
        role: admin
EOF

  timeout 60 sh -ec 'until kubectl -n tenant-test get hr "kubernetes-'"${TEST_NAME}"'" >/dev/null 2>&1; do sleep 2; done'
  timeout 180 sh -ec 'until kubectl -n tenant-test get kamajicontrolplane "kubernetes-'"${TEST_NAME}"'" >/dev/null 2>&1; do sleep 5; done'
}

@test "KamajiControlPlane still carries --authentication-config under CustomConfig" {
  KCP="kubernetes-${TEST_NAME}"
  args=$(kubectl -n tenant-test get kamajicontrolplane "${KCP}" -o jsonpath='{.spec.apiServer.extraArgs}')
  echo "extraArgs: ${args}"
  echo "${args}" | grep -q -- "--authentication-config=/etc/kubernetes/authentication-config/config.yaml"
}

@test "AuthenticationConfiguration Secret carries the tenant-supplied issuer (not cozy)" {
  SEC="kubernetes-${TEST_NAME}-oidc-authn-config"
  # Existence backstop before `kubectl wait` — the HR renders the Secret
  # asynchronously and `kubectl wait` on a non-existent object errors out
  # immediately with NotFound.
  timeout 60 sh -ec 'until kubectl -n tenant-test get secret "'"${SEC}"'" >/dev/null 2>&1; do sleep 2; done'
  kubectl -n tenant-test wait secret "${SEC}" --for=jsonpath='{.metadata.name}'="${SEC}" --timeout=60s
  body=$(kubectl -n tenant-test get secret "${SEC}" -o jsonpath='{.data.config\.yaml}' | base64 -d)
  echo "${body}" | grep -qF "url: https://idp.byo.example.test"
  echo "${body}" | grep -qF "cozystack-byo-${TEST_NAME}"
  ! echo "${body}" | grep -qE 'url: https://keycloak\.[^/]+/realms/cozy'
}

@test "No Keycloak objects are created in cozy under CustomConfig" {
  if ! kubectl api-resources --api-group=v1.edp.epam.com >/dev/null 2>&1; then
    skip "EDP Keycloak operator CRDs not present on this cluster"
  fi
  CID="tenant-test-kubernetes-${TEST_NAME}"
  ! kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${CID}" 2>/dev/null
  ! kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${CID}-audience" 2>/dev/null
}

@test "OIDC kubeconfig Secret is NOT pre-rendered under CustomConfig" {
  # System mode pre-renders <release>-oidc-kubeconfig as a placeholder;
  # CustomConfig does not — the operator distributes the BYO kubeconfig
  # out-of-band from their own IdP configuration.
  ! kubectl -n tenant-test get secret "kubernetes-${TEST_NAME}-oidc-kubeconfig" 2>/dev/null
}
