#!/usr/bin/env bats

# Phase-1 OIDC CustomConfig selector e2e for the Grafana instance,
# render-side only.
#
# What is exercised on a live cluster: that the new
# `spec.oidc.mode: CustomConfig` field is admitted; that cozystack-api
# accepts the updated schema; that the HelmRelease renders; that the
# operator-supplied auth.generic_oauth payload lands on the Grafana
# CR; and, crucially, that NO Keycloak objects are created in the
# `cozy` realm when the tenant brings their own issuer.

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
# OIDC templates is then named `${outer}-system`, i.e. `monitoring-system`.
CR_NAME="monitoring"
INNER_REL="${CR_NAME}-system"
BYO_SECRET="mon-oidc-byo-oauth"

cleanup_mon() {
  kubectl -n tenant-test delete monitoring.apps.cozystack.io "${CR_NAME}" \
    --ignore-not-found --wait=false 2>/dev/null || true
  kubectl -n tenant-test wait monitoring.apps.cozystack.io "${CR_NAME}" \
    --for=delete --timeout=2m 2>/dev/null || true
  kubectl -n tenant-test delete secret "${BYO_SECRET}" \
    --ignore-not-found 2>/dev/null || true
}

cozy_cleanup() { cleanup_mon; }

@test "Monitoring CR accepts spec.oidc.mode=CustomConfig with inline config" {
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
    mode: CustomConfig
    customConfig:
      config:
        client_id: byo-monitoring
        client_secret: byo-secret
        auth_url: https://idp.example.test/auth
        token_url: https://idp.example.test/token
        api_url: https://idp.example.test/userinfo
    users:
      - email: e2e-byo@example.com
        role: Viewer
EOF

  # Outer HR (created by cozystack-operator from the CR) shares the CR name.
  timeout 60 sh -ec 'until kubectl -n tenant-test get hr "'"${CR_NAME}"'" >/dev/null 2>&1; do sleep 2; done'
  # Inner HR (rendered by extra/monitoring) is ${outer}-system.
  timeout 120 sh -ec 'until kubectl -n tenant-test get hr "'"${INNER_REL}"'" >/dev/null 2>&1; do sleep 2; done'
  timeout 180 sh -ec 'until kubectl -n tenant-test get grafana grafana >/dev/null 2>&1; do sleep 5; done'
}

@test "Grafana CR carries the operator's auth.generic_oauth payload verbatim" {
  timeout 60 sh -ec 'until kubectl -n tenant-test get grafana grafana >/dev/null 2>&1; do sleep 2; done'

  client_id=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.client_id}')
  echo "client_id: ${client_id}"
  [ "${client_id}" = "byo-monitoring" ]

  auth_url=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth.auth_url}')
  echo "auth_url: ${auth_url}"
  [ "${auth_url}" = "https://idp.example.test/auth" ]

  # No cozy realm in the picture.
  echo "${auth_url}" | grep -vq "realms/cozy" || {
    echo "auth_url unexpectedly references the cozy realm"
    return 1
  }
}

@test "No KeycloakClient / Scope are created in the cozy realm" {
  # Only meaningful when the EDP CRDs exist on the cluster.
  if ! kubectl api-resources --api-group=v1.edp.epam.com >/dev/null 2>&1; then
    skip "EDP Keycloak operator CRDs not present on this cluster"
  fi

  # clientId helper: printf "%s-%s" .Release.Namespace .Release.Name in
  # the inner chart -> tenant-test-monitoring-system.
  CID="tenant-test-${INNER_REL}"

  # Give any reconciler a moment; then assert neither cozy-realm object
  # exists. Chart-owned KeycloakRealmGroups were removed entirely (see
  # design note in docs/oidc-grafana.md) so no group assertions here.
  sleep 5
  ! kubectl -n tenant-test get keycloakclient.v1.edp.epam.com "${CID}" 2>/dev/null
  ! kubectl -n tenant-test get keycloakclientscope.v1.edp.epam.com "${CID}-audience" 2>/dev/null
}

@test "secretRef variant mounts operator Secret under /etc/grafana/oidc" {
  # Operator-owned Secret carrying a ready-made ini fragment.
  kubectl -n tenant-test create secret generic "${BYO_SECRET}" \
    --from-literal=auth.ini='[auth.generic_oauth]
enabled = true
name = ByoIdp
client_id = byo-monitoring
client_secret = byo-secret
' --dry-run=client -o yaml | kubectl apply -f -

  # Update the Monitoring CR in place: swap customConfig.config (inline)
  # for customConfig.secretRef.name. Delete + immediate apply races
  # kubectl's create-vs-patch decision against the API server's grace-
  # period cache and can hit "NotFound" during patch, so let the operator
  # reconcile the mode change on the same CR instead.
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
    mode: CustomConfig
    customConfig:
      secretRef:
        name: ${BYO_SECRET}
EOF

  timeout 60 sh -ec 'until kubectl -n tenant-test get hr "'"${CR_NAME}"'" >/dev/null 2>&1; do sleep 2; done'
  timeout 120 sh -ec 'until kubectl -n tenant-test get hr "'"${INNER_REL}"'" >/dev/null 2>&1; do sleep 2; done'
  timeout 180 sh -ec 'until kubectl -n tenant-test get grafana grafana >/dev/null 2>&1; do sleep 5; done'

  # The Grafana CR from @test 1 already exists and carries the inline
  # auth.generic_oauth block; wait for the chart to reconcile the swap
  # to secretRef by polling for the target state (volume mount present
  # AND inline block absent) instead of asserting immediately.
  timeout 180 sh -ec '
    until [ -n "$(kubectl -n tenant-test get grafana grafana -o jsonpath="{.spec.deployment.spec.template.spec.containers[?(@.name==\"grafana\")].volumeMounts[?(@.name==\"oidc-custom-ini\")].mountPath}")" ]; do sleep 5; done
  '
  timeout 60 sh -ec '
    until [ -z "$(kubectl -n tenant-test get grafana grafana -o jsonpath="{.spec.config.auth\.generic_oauth}")" ]; do sleep 5; done
  '

  # spec.config carries no auth.generic_oauth section in this branch.
  section=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.config.auth\.generic_oauth}')
  echo "spec.config.auth.generic_oauth: ${section:-<empty>}"
  [ -z "${section}" ]

  # The volume + mount + env are on the Grafana CR's deployment override.
  mount=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.deployment.spec.template.spec.containers[?(@.name=="grafana")].volumeMounts[?(@.name=="oidc-custom-ini")].mountPath}')
  echo "mount: ${mount}"
  [ "${mount}" = "/etc/grafana/oidc" ]

  vol=$(kubectl -n tenant-test get grafana grafana \
    -o jsonpath='{.spec.deployment.spec.template.spec.volumes[?(@.name=="oidc-custom-ini")].secret.secretName}')
  echo "secret volume: ${vol}"
  [ "${vol}" = "${BYO_SECRET}" ]
}
