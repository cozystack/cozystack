#!/usr/bin/env bats
# Unit test: the nats trust anchor reaches tenants as a key-free projection, and
# the two key-BEARING Secrets the chart creates never do.
#
# The chart issues nats-<name>-ca (CA certificate AND CA private key, the latter
# under tls.key) and nats-<name>-tls (server certificate AND server private key).
# Neither may be handed to a tenant: read access to the CA key would let the
# holder issue certificates for anything rather than merely verify the server.
#
# What a client actually needs is ca.crt alone. That is delivered by the
# CA-extraction controller as the key-free <release>.tenant-ca projection,
# selected here by LABEL rather than by name. The label is stamped only on an
# object the controller itself produced, whereas a name grant conveys whatever
# happens to occupy the name.
#
# These assertions exist because both halves are one edit away from breaking
# with a fully green chart suite, for two different reasons. The
# ResourceDefinition is rendered, by the nats-rd chart inlining cozyrds/*, but
# that chart carries neither a tests directory nor the test target
# hack/helm-unit-tests.sh looks for, and no packages/system/*-rd package does.
# The dashboard Role is rendered by a chart that has both, and no suite under
# packages/apps/nats/tests/ asserts on it.
#
# The grants are asserted as WHITELISTS against rendered output rather than as
# a blacklist of forbidden names over source text. A blacklist grepping for
# "{{ .Release.Name }}-ca" is defeated by quoting the scalar, by $.Release.Name,
# or by a fullname helper -- all of which grant the CA private key while the
# guard stays green.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/nats-rd/cozyrds/nats.yaml"
CHART="$REPO_ROOT/packages/apps/nats"

# The complete set of grants the tenant holds, asserted as one value rather than
# per-dimension: a name whitelist alone passes an added label selector, and a
# label whitelist alone passes an added name. Either dimension can reach a
# key-bearing Secret -- controller.cert-manager.io/fao is stamped on both of
# them, so a selector on it is a one-line grant of the CA private key.
EXPECTED_RD_INCLUDE='[{"resourceNames":["nats-{{ .name }}-credentials"]},{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}]'

# The dashboard template's entire RBAC output: every rule of the Role, and the
# kinds it renders. Asserted whole rather than filtered down to the rules that mention
# secrets, because every filter admits the spelling it did not anticipate: a rule
# with no resourceNames names no Secret, a rule with resources ["*"] names no
# resource, and a ClusterRole is not a Role. Each of those reaches both
# key-bearing Secrets. There is no filter that is right for all of them, and one
# comparison against the whole output needs none.
EXPECTED_ROLE_RULES='[{"apiGroups":[""],"resources":["services"],"resourceNames":["nats-test"],"verbs":["get","list","watch"]},{"apiGroups":[""],"resources":["secrets"],"resourceNames":["nats-test-credentials"],"verbs":["get","list","watch"]},{"apiGroups":["cozystack.io"],"resources":["workloadmonitors"],"resourceNames":["nats-test"],"verbs":["get","list","watch"]}]'
EXPECTED_RBAC_KINDS='["Role","RoleBinding"]'

# Subsumed by the whitelist below, which cannot match without this entry. Kept
# because the runner stops at the first failing test, so this one fires first
# and says what the loss costs rather than printing two JSON blobs to diff.
@test "nats-rd cozyrds selects the key-free tenant-CA projection by label" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  count=$(yq eval \
    '[.spec.secrets.include[] | select(.matchLabels."internal.cozystack.io/tenant-ca" == "true")] | length' \
    "$COZYRDS") || exit 1

  if [ "$count" -lt 1 ]; then
    echo 'nats-rd does not select internal.cozystack.io/tenant-ca: "true"' >&2
    echo "Without it the tenant cannot read ca.crt and TLS verification is impossible." >&2
    exit 1
  fi
}

@test "nats-rd cozyrds grants nothing beyond the credentials Secret and the trust anchor" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  include=$(yq eval --output-format=json --indent=0 '.spec.secrets.include' "$COZYRDS") || exit 1

  if [ "$include" != "$EXPECTED_RD_INCLUDE" ]; then
    echo "Unexpected nats-rd spec.secrets.include: $include" >&2
    echo "Expected exactly: $EXPECTED_RD_INCLUDE" >&2
    echo "nats-<name>-ca holds the CA private key and nats-<name>-tls the server key;" >&2
    echo "neither may be granted to a tenant, by name or by label." >&2
    exit 1
  fi
}

# Asserted against RENDERED output, so the spelling used in the template --
# quoted, helper-derived, or otherwise -- cannot smuggle a name past the check.
@test "nats dashboard Role renders exactly the grants it is supposed to" {
  if [ ! -d "$CHART" ]; then
    echo "Chart not found at $CHART -- did it move?" >&2
    exit 1
  fi

  # stderr is deliberately not discarded: when the render breaks, helm's own
  # message is the only thing that says why, and a CI red showing just
  # "helm template failed" sends the reader back here to rerun it by hand.
  rendered=$(helm template nats-test "$CHART" \
    --namespace tenant-test \
    --set '_cluster.cluster-domain=cozy.local' \
    --show-only templates/dashboard-resourcemap.yaml) || {
    echo "helm template failed for $CHART" >&2
    exit 1
  }

  # eval-all aggregates the documents into one result, so the Role may sit at any
  # position among them.
  kinds=$(printf '%s\n' "$rendered" | yq eval-all --output-format=json --indent=0 \
    '[.kind]' -) || exit 1

  if [ "$kinds" != "$EXPECTED_RBAC_KINDS" ]; then
    echo "Unexpected RBAC kinds rendered by the nats dashboard template: $kinds" >&2
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
    echo "Unexpected rules in the nats dashboard Role: $rules" >&2
    echo "Expected exactly: $EXPECTED_ROLE_RULES" >&2
    echo "nats-<release>-ca holds the CA PRIVATE KEY and nats-<release>-tls the" >&2
    echo "server private key. The trust anchor reaches the tenant as the key-free" >&2
    echo "<release>.tenant-ca projection via tenantsecrets, not by a Role grant." >&2
    exit 1
  fi
}
