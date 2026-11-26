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
# These assertions exist because the grant is one edit away from breaking with
# a fully green chart suite. The ResourceDefinition is rendered by the rabbitmq-rd
# chart inlining cozyrds/*, and that chart carries no tests directory.
#
# The grants are asserted as WHITELISTS against whole values rather than as a
# blacklist of forbidden names over source text. A blacklist grepping for
# "rabbitmq-{{ .name }}-ca" is defeated by quoting the scalar, by a different
# templating of the name, or by a label selector that happens to match it -- all
# of which grant a private key while the guard stays green.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/rabbitmq-rd/cozyrds/rabbitmq.yaml"

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
