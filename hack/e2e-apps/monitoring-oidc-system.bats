#!/usr/bin/env bats

# Phase-1 OIDC System-mode e2e for the Grafana instance, render-side
# only.
#
# Pairs with monitoring-oidc-customconfig.bats (BYO issuer path).
#
# Does NOT drive a full browser login through Keycloak: that path is
# deferred to a follow-up integration suite (same posture as the
# tenant kube-apiserver's kubernetes-oidc-system.bats).
#
# What is exercised here on a live cluster: that the new
# `spec.oidc.*` fields are admitted by the Monitoring CR; that
# cozystack-api accepts the updated schema; that the HelmRelease
# renders the OIDC templates; and that the resulting KeycloakClient /
# KeycloakClientScope / KeycloakRealmGroups / persistent
# client-secret Secret / Grafana CR carry the expected shape.

# cozytest.sh (the e2e runner) is not real bats: it never invokes
# setup()/teardown(). Cleanup belongs in cozy_cleanup(), which runs at
# suite exit and on the first failing test. Per-test isolation is done
# inline at the top of each @test.
#
# Naming: the extra/monitoring chart is a singleton per tenant
# namespace and enforces `.Release.Name == "monitoring"` in
# templates/check-release-name.yaml. So the Monitoring CR (and its
# operator-produced outer HelmRelease) MUST be named `monitoring`;
# the inner packages/system/monitoring HelmRelease that carries the
# OIDC templates is then named `${outer}-system`, i.e. `monitoring-system`,
# and every OIDC identifier derived by the helpers (clientId, audience
# scope, groups, client-secret Secret) is built off that inner name.
CR_NAME="monitoring"
INNER_REL="${CR_NAME}-system"
CID="tenant-test-${INNER_REL}"

cleanup_mon() {
  kubectl -n tenant-test delete monitoring.apps.cozystack.io "${CR_NAME}" \
    --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait monitoring.apps.cozystack.io "${CR_NAME}" \
    --for=delete --timeout=2m 2>/dev/null || true
}

cozy_cleanup() { cleanup_mon; }

@test "Monitoring CR accepts spec.oidc.mode=System" {
  cleanup_mon
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Monitoring
metadata:
  name: ${CR_NAME}
  namespace: tenant-test
spec:
  host: ""
  metricsStorages:
    - name: shortterm
      retentionPeriod: "3d"
      deduplicationInterval: "15s"
      storage: 10Gi
      storageClassName: replicated
  logsStorages:
    - name: generic
      retentionPeriod: "1"
      storage: 10Gi
      storageClassName: replicated
  oidc:
    mode: System
EOF

  # Outer HR shares the CR name; inner HR (chart target) is ${outer}-system.
  timeout 60 sh -ec 'until kubectl -n tenant-test get hr "'"${CR_NAME}"'" >/dev/null 2>&1; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl -n tenant-test get hr "'"${INNER_REL}"'" >/dev/null 2>&1; do sleep 2; done'

  # Wait for the chart to render its Grafana CR. We do not wait on
  # HR Ready: that requires a full VictoriaMetrics + Postgres bringup
  # which the render-side test deliberately avoids.
  timeout 180 sh -ec 'until kubectl -n tenant-test get grafana grafana >/dev/null 2>&1; do sleep 5; done'
}

@test "Grafana CR carries auth.generic_oauth pointing at the cozy realm" {
  timeout 60 sh -ec 'until kubectl -n tenant-test get grafana grafana >/dev/null 2>&1; do sleep 2; done'

  # auth.generic_oauth is a top-level key under spec.config; the key
  # name literally contains a dot, so jsonpath needs a bracketed lookup.
  enabled=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.enabled}')
  echo "generic_oauth.enabled: ${enabled}"
  [ "${enabled}" = "true" ]

  client_id=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.client_id}')
  echo "client_id: ${client_id}"
  [ "${client_id}" = "${CID}" ]

  auth_url=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.auth_url}')
  echo "auth_url: ${auth_url}"
  echo "${auth_url}" | grep -qE '/realms/cozy/protocol/openid-connect/auth$'
}

@test "Persistent client-secret Secret is created and 32-char random" {
  # clientSecretName helper: printf "%s-oidc-client" .Release.Name -> ${INNER_REL}-oidc-client.
  SEC="${INNER_REL}-oidc-client"
  timeout 60 sh -ec 'until kubectl -n tenant-test get secret "'"${SEC}"'" >/dev/null 2>&1; do sleep 2; done'
  value=$(kubectl -n tenant-test get secret "${SEC}" \
    -o jsonpath='{.data.client-secret}' | base64 -d)
  echo "client-secret length: ${#value}"
  [ "${#value}" = "32" ]
  # No unexpected characters.
  echo "${value}" | grep -qE '^[A-Za-z0-9]+$'
}

@test "Per-instance KeycloakClient + KeycloakClientScope + 3 KeycloakRealmGroups land in cozy" {
  # The EDP Keycloak operator API group is only present when the
  # platform-level OIDC feature is on. Skip gracefully otherwise so the
  # test suite works on runners without Keycloak installed.
  if ! kubectl api-resources --api-group=v1.edp.epam.com >/dev/null 2>&1; then
    skip "EDP Keycloak operator CRDs not present on this cluster"
  fi

  timeout 60 sh -ec 'until kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "'"${CID}"'" >/dev/null 2>&1; do sleep 2; done'
  public=$(kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${CID}" \
    -o jsonpath='{.spec.public}')
  echo "client public: ${public}"
  [ "${public}" = "false" ]

  timeout 30 sh -ec 'until kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "'"${CID}"'-audience" >/dev/null 2>&1; do sleep 2; done'
  mapper=$(kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${CID}-audience" \
    -o jsonpath='{.spec.protocolMappers[0].protocolMapper}')
  echo "audience mapper: ${mapper}"
  [ "${mapper}" = "oidc-audience-mapper" ]

  for role in admin editor viewer; do
    timeout 30 sh -ec 'until kubectl -n tenant-test get keycloakrealmgroup.v1.edp.epam.com "'"${CID}-${role}"'" >/dev/null 2>&1; do sleep 2; done'
    echo "group ${CID}-${role} present"
  done
}

@test "Grafana Deployment injects GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET from the Secret" {
  # Grafana operator materialises a Deployment named `grafana-deployment`
  # from the Grafana CR. Poll for it before asserting on the env.
  timeout 120 sh -ec 'until kubectl -n tenant-test get deploy grafana-deployment >/dev/null 2>&1; do sleep 5; done'
  envs=$(kubectl -n tenant-test get deploy grafana-deployment \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="grafana")].env[*].name}')
  echo "grafana env: ${envs}"
  echo "${envs}" | grep -qw "GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET"

  ref=$(kubectl -n tenant-test get deploy grafana-deployment \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="grafana")].env[?(@.name=="GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET")].valueFrom.secretKeyRef.name}')
  echo "client-secret env source: ${ref}"
  [ "${ref}" = "${INNER_REL}-oidc-client" ]
}
