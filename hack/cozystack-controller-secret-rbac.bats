#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Semantic guard on what packages/system/cozystack-controller grants over core
# Secrets.
#
# The union is over every ClusterRole and Role the chart renders; bindings are
# never consulted. Today that is the same question as "what does the controller's
# own ServiceAccount hold", because the chart's ClusterRoleBinding and RoleBinding
# both name the cozystack-controller ServiceAccount and nothing else. A role added
# here for some other subject would fold into the union too, which errs toward
# refusing a grant rather than missing one.
#
# Two reconcilers share one Secret write grant: WildcardSecretReconciler
# replicates the operator wildcard TLS Secret into each tenant namespace, and
# CACertReconciler writes -- and withdraws -- the key-free "<release>.tenant-ca"
# projection. The controller is cluster-scoped, so any verb it holds on Secrets
# it holds on every Secret in every tenant namespace. That is why the set is
# pinned rather than merely bounded from above: a widening reaches all tenants,
# and a narrowing breaks both reconcilers.
#
# The check reads effective permission off the rendered manifests, not manifest
# text. A rule reaches core Secrets when its apiGroups admit "" and its
# resources admit "secrets" -- by name or via "*" on either axis -- and a "*" in
# its verbs expands to the verb set the API server defines for a namespaced
# resource. The union over every ClusterRole and Role the chart renders must
# equal the declared set exactly.
#
# Expansion is what makes the guard hold on the chart as it stands: the chart
# ends with a blanket `apiGroups: ['*'] resources: ['*'] verbs: [get,list,watch]`
# read rule, which does reach Secrets and whose verbs are already inside the
# declared set. A guard written as "no wildcard anywhere near secrets" would be
# red on an unmodified tree.
#
# The union is over the chart's default rendering: `helm template` is invoked
# with no --values, so a rule that appeared only under some values file would go
# unseen. templates/rbac.yaml carries no templating at all today, so nothing is
# gated behind a value; render the variants here too if that changes.
#
# The check lives here rather than beside the chart's helm-unittest suite because
# helm-unittest compares every field it is handed by value equality. `any: true`
# changes which fields are compared, not how they are compared, so neither the
# whole-rule form nor the `any: true` one can bound what a verbs list may hold --
# `verbs: ["get", "*"]` slips past both. That primitive is the right one for
# pinning a rule as written, which tests/rbac_test.yaml does, and it cannot
# express an upper bound: a wildcard in resources, a wildcard in apiGroups, a "*"
# appended to the declared verb list, an extra field beside the verbs, and a
# wider grant added as a separate rule while the declared one stays untouched all
# pass it.
#
# resourceNames is deliberately ignored. A rule scoped to named Secrets that
# grants "*" on them is still a wildcard verb grant, so the union counts it.
#
# An aggregated ClusterRole is the one widening this cannot see: it renders with
# no `rules` of its own, so whatever the aggregation confers stays outside the
# union. This chart declares no aggregationRule; a rule set assembled that way
# would need its own check.
#
# The assertion is an equality against a non-empty expected set, so a chart that
# fails to render, or a yq/jq breakage that yields nothing, fails the test
# rather than passing it vacuously.
#
# Requires: helm, yq (mikefarah v4+), jq -- the same three
# hack/admin-kubeconfig-invariant.bats needs to render and inspect a chart.
#
# Run with: hack/cozytest.sh hack/cozystack-controller-secret-rbac.bats
# -----------------------------------------------------------------------------

# What "*" in a rule's verbs means for a namespaced resource such as Secret.
VERB_UNIVERSE='["create","delete","deletecollection","get","list","patch","update","watch"]'

# The grant the chart declares, as a sorted set: read for both reconcilers plus
# the writes the two of them need. Sorted because the comparison below is on
# `unique`d jq output, which is sorted.
DECLARED_SECRET_VERBS="create,delete,get,list,patch,update,watch"

@test "cozystack-controller grants exactly the declared verb set on core Secrets in any spelling" {
  chart="packages/system/cozystack-controller"
  [ -d "$chart" ]

  tmp=$(mktemp -d)

  # helm's stderr is left alone: this chart renders silently, so there is nothing
  # to suppress, and a chart that stops rendering should say why in the log
  # rather than fail this line mutely.
  helm template cozystack-controller "$chart" \
    --namespace cozy-system \
    > "$tmp/rendered.yaml"

  # yq streams one JSON object per rendered document; jq -s slurps the stream so
  # the rules of every ClusterRole and Role are one collection.
  yq --output-format=json eval-all '.' "$tmp/rendered.yaml" \
    | jq -s '
        map(select(.kind == "ClusterRole" or .kind == "Role"))
        | map(.rules // []) | add // []
        | map(select(
            ((.apiGroups // []) | any(. == "" or . == "*")) and
            ((.resources // []) | any(. == "secrets" or . == "*"))
          ))
      ' > "$tmp/secret-rules.json"

  effective=$(
    jq --raw-output --argjson universe "$VERB_UNIVERSE" '
      map(if ((.verbs // []) | any(. == "*")) then $universe else (.verbs // []) end)
      | add // [] | unique | join(",")
    ' "$tmp/secret-rules.json"
  )

  if [ "$effective" != "$DECLARED_SECRET_VERBS" ]; then
    echo "Effective verb set on core Secrets is not the declared one." >&2
    echo "  declared: $DECLARED_SECRET_VERBS" >&2
    echo "  rendered: $effective" >&2
    echo "Rules reaching core Secrets:" >&2
    cat "$tmp/secret-rules.json" >&2
    exit 1
  fi

  echo "Effective verbs on core Secrets: $effective"
  rm -rf "$tmp"
}
