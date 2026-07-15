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
# KeycloakClientScope / persistent client-secret Secret / Grafana CR /
# users-reconcile Job carry the expected shape. Chart-owned
# KeycloakRealmGroups + role_attribute_path are gone (see the
# design note in docs/oidc-grafana.md); authorization is driven by
# spec.oidc.users reconciled into Grafana orgs by the users-Job.

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
# scope, client-secret Secret) is built off that inner name.
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
    users:
      - email: e2e-admin@example.com
        role: Admin
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
  # Wait for the Grafana CR to actually reflect mode=System values —
  # `until kubectl get grafana` only proves the CR EXISTS (it may still
  # be a stale copy from a previous test or from before the inner HR
  # picked up the new values). Poll the specific field the test is
  # about to assert on so the race window closes.
  timeout 180 sh -ec '
    until [ "$(kubectl -n tenant-test get grafana grafana \
      -o jsonpath="{.spec.config.auth\.generic_oauth.enabled}" 2>/dev/null)" = "true" ]; do
      sleep 5
    done
  '

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

@test "Per-instance KeycloakClient + KeycloakClientScope land in cozy (no groups)" {
  # The EDP Keycloak operator API group is only present when the
  # platform-level OIDC feature is on. Skip gracefully otherwise so the
  # test suite works on runners without Keycloak installed.
  if ! kubectl api-resources --api-group=v1.edp.epam.com >/dev/null 2>&1; then
    skip "EDP Keycloak operator CRDs not present on this cluster"
  fi

  timeout 60 sh -ec 'until kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "'"${CID}"'" >/dev/null 2>&1; do sleep 2; done'
  # The EDP Keycloak operator's CRD strips `spec.public: false` on write
  # (schema default is false, so a false value never materialises in the
  # applied object). Assert confidentiality via clientAuthenticatorType,
  # which the operator DOES persist.
  auth_type=$(kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${CID}" \
    -o jsonpath='{.spec.clientAuthenticatorType}')
  echo "client authenticator type: ${auth_type}"
  [ "${auth_type}" = "client-secret" ]

  timeout 30 sh -ec 'until kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "'"${CID}"'-audience" >/dev/null 2>&1; do sleep 2; done'
  mapper=$(kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${CID}-audience" \
    -o jsonpath='{.spec.protocolMappers[0].protocolMapper}')
  echo "audience mapper: ${mapper}"
  [ "${mapper}" = "oidc-audience-mapper" ]

  # No chart-owned KeycloakRealmGroups — directory objects stay owned
  # by whoever runs the cozy realm; authorization is app-side (see the
  # users-Job assertion below).
  for role in admin editor viewer; do
    if kubectl -n tenant-test get keycloakrealmgroup.v1.edp.epam.com "${CID}-${role}" >/dev/null 2>&1; then
      echo "unexpected chart-owned realm group ${CID}-${role} present" >&2
      false
    fi
  done
}

@test "Grafana config has no role_attribute_path and enables skip_org_role_sync" {
  # Same wait as the first Grafana-CR test — poll the specific field
  # rather than merely the CR's existence so we don't hit a stale copy.
  timeout 180 sh -ec '
    until [ "$(kubectl -n tenant-test get grafana grafana \
      -o jsonpath="{.spec.config.auth\.generic_oauth.skip_org_role_sync}" 2>/dev/null)" = "true" ]; do
      sleep 5
    done
  '

  role_attr=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.role_attribute_path}')
  echo "role_attribute_path: ${role_attr}"
  [ -z "${role_attr}" ]

  skip_sync=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.skip_org_role_sync}')
  echo "skip_org_role_sync: ${skip_sync}"
  [ "${skip_sync}" = "true" ]

  email_lookup=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.oauth_allow_insecure_email_lookup}')
  echo "oauth_allow_insecure_email_lookup: ${email_lookup}"
  [ "${email_lookup}" = "true" ]
}

@test "users-Job runs and carries the desired list from spec.oidc.users" {
  # helm.sh/hook: post-install,post-upgrade — the Job appears after the
  # inner HR reconciles. `hook-delete-policy: before-hook-creation`
  # keeps the previous Job around across upgrades, so `until kubectl
  # get job` may return a stale Job from the previous test (or from
  # the customconfig round where users was set differently or absent).
  # Poll on the target env value directly — the new Job with our
  # desired list is the first Job whose DESIRED_USERS_JSON contains
  # `e2e-admin@example.com`.
  timeout 600 sh -ec '
    until kubectl -n tenant-test get job "'"${INNER_REL}"'-oidc-users" \
      -o jsonpath="{.spec.template.spec.containers[?(@.name==\"reconcile\")].env[?(@.name==\"DESIRED_USERS_JSON\")].value}" 2>/dev/null \
      | grep -q "e2e-admin@example.com"; do
      sleep 5
    done
  '

  desired=$(kubectl -n tenant-test get job "${INNER_REL}-oidc-users" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="reconcile")].env[?(@.name=="DESIRED_USERS_JSON")].value}')
  echo "DESIRED_USERS_JSON: ${desired}"
  echo "${desired}" | grep -q 'e2e-admin@example.com'
  echo "${desired}" | grep -q '"role":"Admin"'

  mode=$(kubectl -n tenant-test get job "${INNER_REL}-oidc-users" \
    -o jsonpath='{.spec.template.spec.containers[?(@.name=="reconcile")].env[?(@.name=="OIDC_MODE")].value}')
  echo "OIDC_MODE: ${mode}"
  [ "${mode}" = "System" ]
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
