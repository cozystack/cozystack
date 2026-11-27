#!/usr/bin/env bats
# Unit test: no application chart grants tenant subjects read on core Secrets.
#
# Tenants read the Secrets an application exposes through
# core.cozystack.io/tenantsecrets. cozystack-api serves that resource with its
# own identity and only for Secrets the ApplicationDefinition's
# secrets.include admits, so the allowlist lives in one place, the -rd package.
# A Role in an application chart that grants core `secrets` to the tenant's
# groups or ServiceAccount bypasses that allowlist entirely. The per-chart
# dashboard Roles did exactly that, and one of them rendered an empty
# resourceNames list, which RBAC reads as every Secret in the namespace.
#
# The check renders every chart under packages/apps and packages/extra and
# inspects the rendered output rather than the template source: a grant can be
# spelled through a helper, a quoted scalar or a range, and only the rendered
# object says what RBAC will see. A chart that fails to render fails the test,
# so a chart cannot drop out of the check unnoticed.
#
# What this does not reach: a grant gated on a value the fixtures leave unset,
# and a grant behind a lookup or a .Capabilities check, since helm template has
# no cluster.

load test_helper

FIXTURE_DIR="hack/testdata/render-fixtures"

# A subject is a tenant's when it is one of the groups cozy-lib's
# subjectsForTenantAndAccessLevel emits or the tenant ServiceAccount, all of
# which are named after a tenant-* namespace. A rule grants Secrets when it
# names the core group (or every group) and the secrets resource (or every
# resource); the verbs do not matter, since get alone is a read of the value.
OFFENDING_GRANTS_JQ='
  def tenant_subject: (.kind == "Group" or .kind == "ServiceAccount") and ((.name // "") | startswith("tenant-"));
  def grants_secrets: ((.apiGroups // []) | any(. == "" or . == "*")) and ((.resources // []) | any(. == "secrets" or . == "*"));
  map(select(. != null)) as $docs
  | [$docs[] | select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding")
      | select((.subjects // []) | any(tenant_subject)) | .roleRef | {kind, name}] as $refs
  | [$docs[] | select(.kind == "Role" or .kind == "ClusterRole")
      | select(({kind, name: .metadata.name} as $r | $refs | index([$r]))
          or ((.metadata.labels // {}) | keys | any(startswith("rbac.cozystack.io/aggregate-to-tenant"))))
      | {kind, name: .metadata.name, rules: [(.rules // [])[] | select(grants_secrets)]}
      | select(.rules | length > 0)]
'

# render <chart-dir> <fixture>
# Release names and values satisfy each chart's own render-time contract
# (fixed release name, fixed namespace, a required value, or a cluster lookup
# that sizing by resources avoids). Output is a JSON array of documents.
render() {
  # Called inside a command substitution, so this reaches only that subshell.
  # Without it a failed render feeds yq nothing and reads as a chart with no RBAC.
  set -o pipefail
  chart=$1 fixture=$2
  name=$(basename "$chart")
  namespace=tenant-test
  case "$name" in
    bootbox) set -- bootbox; namespace=tenant-root ;;
    computeplane|etcd|info|ingress|monitoring|seaweedfs|gateway) set -- "$name" ;;
    external-dns) set -- external-dns --set provider=inmemory ;;
    kubernetes-nodes) set -- kubernetes-nodes-render-check --set-string cluster=render \
      --set-string resources.cpu=2 --set-string resources.memory=4Gi ;;
    tenant) set -- tenant-sub --api-versions v1.edp.epam.com/v1 ;;
    vm-instance) set -- vm-instance-render-check --set-string instanceType= \
      --set-string instanceProfile= --set-string resources.cpu=1 \
      --set-string resources.sockets=1 --set-string resources.memory=1Gi ;;
    *) set -- "$name-render-check" ;;
  esac
  helm template "$@" "$chart" --namespace "$namespace" --values "$fixture" \
    | yq eval-all --output-format=json --indent=0 '[.]' -
}

@test "the detector flags a Role that grants tenant groups core Secrets" {
  # Guards the guard: a jq filter that silently matched nothing would keep the
  # repository-wide check below green forever.
  manifest='[
    {"kind":"Role","metadata":{"name":"x-dashboard-resources"},"rules":[{"apiGroups":[""],"resources":["secrets"],"verbs":["get"]}]},
    {"kind":"RoleBinding","metadata":{"name":"x-dashboard-resources"},"subjects":[{"kind":"Group","name":"tenant-test-use"}],"roleRef":{"kind":"Role","name":"x-dashboard-resources"}}
  ]'
  found=$(printf '%s' "$manifest" | jq --compact-output "$OFFENDING_GRANTS_JQ")
  if [ "$found" = "[]" ]; then
    echo "The detector missed a Role granting core secrets to a tenant group" >&2
    exit 1
  fi
}

@test "no chart under packages/apps or packages/extra grants tenant subjects core Secrets" {
  failed=0 checked=0
  for chart in packages/apps/*/ packages/extra/*/; do
    for fixture in "$FIXTURE_DIR"/*.yaml; do
      state=$(basename "$fixture" .yaml)
      if ! docs=$(render "$chart" "$fixture"); then
        echo "FAIL $chart ($state): render failed" >&2
        failed=1
        continue
      fi
      found=$(printf '%s' "$docs" | jq --compact-output "$OFFENDING_GRANTS_JQ") || {
        echo "FAIL $chart ($state): could not evaluate the rendered RBAC" >&2
        failed=1
        continue
      }
      if [ "$found" != "[]" ]; then
        echo "FAIL $chart ($state) grants tenant subjects core Secrets: $found" >&2
        echo "Expose the Secret through the -rd secrets.include instead; tenants read it as a tenantsecret." >&2
        failed=1
      fi
      checked=$((checked + 1))
    done
  done
  if [ "$checked" -eq 0 ]; then
    echo "No chart was rendered; did packages/apps or $FIXTURE_DIR move?" >&2
    exit 1
  fi
  [ "$failed" -eq 0 ]
}
