#!/usr/bin/env bats
# Unit test: the rabbitmq trust anchor reaches tenants as a key-free projection,
# and the key-BEARING Secrets around it never do.
#
# The chart issues rabbitmq-<name>-ca (CA certificate AND CA private key) and
# rabbitmq-<name>-tls (server certificate AND server private key); the operator
# issues rabbitmq-<name>-erlang-cookie, the cluster's shared secret. None of the
# three may be handed to a tenant: read access to the CA key would let the holder
# issue certificates for anything rather than merely verify the server.
#
# What a client needs is ca.crt alone. cluster-operator produces no CA object of
# its own (spec.tls.caSecretName is an input it only consumes), so the chart mints
# the CA through cert-manager and names that Secret in a TenantProjection
# sentinel. The label selected on below is stamped by the CA-extraction
# controller on the projection it writes, never on the CA Secret.
#
# These assertions exist because both halves are one edit away from breaking with
# a fully green chart suite. The ResourceDefinition is rendered by the rabbitmq-rd
# chart inlining cozyrds/*, and that chart carries no tests directory; the
# dashboard Role is rendered by a chart that has one, but no suite there asserts
# on the whole Role.
#
# The grants are asserted as WHITELISTS against whole values rather than as a
# blacklist of forbidden names over source text. A blacklist grepping for
# "rabbitmq-{{ .name }}-ca" is defeated by quoting the scalar, by a different
# templating of the name, or by a label selector that happens to match it -- all
# of which grant a private key while the guard stays green.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/rabbitmq-rd/cozyrds/rabbitmq.yaml"
CHART="$REPO_ROOT/packages/apps/rabbitmq"

# The complete set of grants the tenant holds, asserted as one value rather than
# per-dimension: a name whitelist alone passes an added label selector, and a
# label whitelist alone passes an added name. Either dimension reaches a
# key-bearing Secret -- controller.cert-manager.io/fao is stamped on both the CA
# and the leaf, so a selector on it is a one-line grant of the CA private key.
EXPECTED_RD_INCLUDE='[{"resourceNames":["rabbitmq-{{ .name }}-default-user"]},{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}},{"matchLabels":{"apps.cozystack.io/user-secret":"true"}}]'

# exclude is evaluated before include and wins, so it is the structural backstop
# against a mis-stamped label on a key-bearing object. Asserted whole for the same
# reason as the include: dropping one entry is the edit that has to be caught.
EXPECTED_RD_EXCLUDE='[{"resourceNames":["rabbitmq-{{ .name }}-ca","rabbitmq-{{ .name }}-tls","rabbitmq-{{ .name }}-erlang-cookie"]}]'

# The dashboard template's entire RBAC output: every rule of the Role, and the
# kinds it renders. Asserted whole rather than filtered down to the rules that
# mention secrets, because every filter admits the spelling it did not anticipate:
# a rule with no resourceNames names no Secret, a rule with resources ["*"] names
# no resource, and a ClusterRole is not a Role. Each of those reaches both
# key-bearing Secrets, and one comparison against the whole output needs no filter.
EXPECTED_RBAC_KINDS='["Role","RoleBinding"]'
EXPECTED_ROLE_RULES='[{"apiGroups":[""],"resources":["secrets"],"resourceNames":["rabbitmq-test-default-user"],"verbs":["get","list","watch"]},{"apiGroups":[""],"resources":["services"],"resourceNames":["rabbitmq-test"],"verbs":["get","list","watch"]},{"apiGroups":["cozystack.io"],"resources":["workloadmonitors"],"resourceNames":["rabbitmq-test"],"verbs":["get","list","watch"]}]'

# The secrets rule is the one place in this Role that grows from values, so the
# whole-value assertion above only covers it while users is empty. This pins what
# a declared user is allowed to add: its own credentials Secret and nothing else.
EXPECTED_ROLE_RULES_WITH_USER='[{"apiGroups":[""],"resources":["secrets"],"resourceNames":["rabbitmq-test-default-user","rabbitmq-test-alice-credentials"],"verbs":["get","list","watch"]},{"apiGroups":[""],"resources":["services"],"resourceNames":["rabbitmq-test"],"verbs":["get","list","watch"]},{"apiGroups":["cozystack.io"],"resources":["workloadmonitors"],"resourceNames":["rabbitmq-test"],"verbs":["get","list","watch"]}]'

render_rbac() {
  # stderr is deliberately not discarded: when the render breaks, helm's own
  # message is the only thing that says why, and a CI red showing just
  # "helm template failed" sends the reader back here to rerun it by hand.
  helm template rabbitmq-test "$CHART" \
    --namespace tenant-test \
    --set '_cluster.cluster-domain=cozy.local' \
    "$@" \
    --show-only templates/dashboard-resourcemap.yaml
}

# Subsumed by the whitelist below, which cannot match without this entry. Kept
# because the runner stops at the first failing test, so this one fires first and
# says what the loss costs rather than printing two JSON blobs to diff.
@test "rabbitmq-rd cozyrds selects the key-free tenant-CA projection by label" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  count=$(yq eval \
    '[.spec.secrets.include[] | select(.matchLabels."internal.cozystack.io/tenant-ca" == "true")] | length' \
    "$COZYRDS") || exit 1

  if [ "$count" -lt 1 ]; then
    echo 'rabbitmq-rd does not select internal.cozystack.io/tenant-ca: "true"' >&2
    echo "Without it the tenant cannot read ca.crt and TLS verification is impossible." >&2
    exit 1
  fi
}

@test "rabbitmq-rd cozyrds grants nothing beyond the default user, the trust anchor and user credentials" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  include=$(yq eval --output-format=json --indent=0 '.spec.secrets.include' "$COZYRDS") || exit 1

  if [ "$include" != "$EXPECTED_RD_INCLUDE" ]; then
    echo "Unexpected rabbitmq-rd spec.secrets.include: $include" >&2
    echo "Expected exactly: $EXPECTED_RD_INCLUDE" >&2
    echo "rabbitmq-<name>-ca holds the CA private key and rabbitmq-<name>-tls the" >&2
    echo "server key; neither may be granted to a tenant, by name or by label." >&2
    exit 1
  fi
}

@test "rabbitmq-rd cozyrds keeps the key-bearing Secrets excluded" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  exclude=$(yq eval --output-format=json --indent=0 '.spec.secrets.exclude' "$COZYRDS") || exit 1

  if [ "$exclude" != "$EXPECTED_RD_EXCLUDE" ]; then
    echo "Unexpected rabbitmq-rd spec.secrets.exclude: $exclude" >&2
    echo "Expected exactly: $EXPECTED_RD_EXCLUDE" >&2
    echo "exclude wins over include, so it is what stops a mis-stamped tenant-ca" >&2
    echo "label on the CA, the leaf or the erlang cookie from reaching a tenant." >&2
    exit 1
  fi
}

# Asserted against RENDERED output, so the spelling used in the template --
# quoted, helper-derived, or otherwise -- cannot smuggle a name past the check.
@test "rabbitmq dashboard Role renders exactly the grants it is supposed to" {
  if [ ! -d "$CHART" ]; then
    echo "Chart not found at $CHART -- did it move?" >&2
    exit 1
  fi

  rendered=$(render_rbac) || {
    echo "helm template failed for $CHART" >&2
    exit 1
  }

  # eval-all aggregates the documents into one result, so the Role may sit at any
  # position among them.
  kinds=$(printf '%s\n' "$rendered" | yq eval-all --output-format=json --indent=0 \
    '[.kind]' -) || exit 1

  if [ "$kinds" != "$EXPECTED_RBAC_KINDS" ]; then
    echo "Unexpected RBAC kinds rendered by the rabbitmq dashboard template: $kinds" >&2
    echo "Expected exactly: $EXPECTED_RBAC_KINDS" >&2
    echo "A ClusterRole here would grant across namespaces and is not covered by" >&2
    echo "the rule comparison below, which reads the namespaced Role." >&2
    exit 1
  fi

  # Fails closed on an empty list: a Role that stopped granting anything, or a
  # render that silently produced nothing, does not match the expected rules.
  rules=$(printf '%s\n' "$rendered" | yq eval-all --output-format=json --indent=0 \
    '[select(.kind == "Role") | .rules[]]' -) || exit 1

  if [ "$rules" != "$EXPECTED_ROLE_RULES" ]; then
    echo "Unexpected rules in the rabbitmq dashboard Role: $rules" >&2
    echo "Expected exactly: $EXPECTED_ROLE_RULES" >&2
    echo "rabbitmq-<release>-ca holds the CA PRIVATE KEY, rabbitmq-<release>-tls the" >&2
    echo "server private key, and rabbitmq-<release>-erlang-cookie the cluster secret." >&2
    echo "The trust anchor reaches the tenant as the key-free <release>.tenant-ca" >&2
    echo "projection via tenantsecrets, not by a Role grant." >&2
    exit 1
  fi
}

@test "a declared rabbitmq user adds only its own credentials Secret to the Role" {
  if [ ! -d "$CHART" ]; then
    echo "Chart not found at $CHART -- did it move?" >&2
    exit 1
  fi

  rendered=$(render_rbac --set 'users.alice.password=placeholder') || {
    echo "helm template failed for $CHART" >&2
    exit 1
  }

  rules=$(printf '%s\n' "$rendered" | yq eval-all --output-format=json --indent=0 \
    '[select(.kind == "Role") | .rules[]]' -) || exit 1

  if [ "$rules" != "$EXPECTED_ROLE_RULES_WITH_USER" ]; then
    echo "Unexpected rules with one declared user: $rules" >&2
    echo "Expected exactly: $EXPECTED_ROLE_RULES_WITH_USER" >&2
    echo "The secrets rule is the only part of this Role that grows from values," >&2
    echo "so a name reaching it from the users loop is granted to the tenant." >&2
    exit 1
  fi
}
