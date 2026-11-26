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
# These assertions exist because the grant is one edit away from breaking with
# a fully green chart suite. The ResourceDefinition is rendered by the nats-rd
# chart inlining cozyrds/*, but that chart carries neither a tests directory nor
# the test target hack/helm-unit-tests.sh looks for.
#
# The grants are asserted as WHITELISTS against whole values rather than as
# a blacklist of forbidden names over source text. A blacklist grepping for
# "{{ .Release.Name }}-ca" is defeated by quoting the scalar, by $.Release.Name,
# or by a fullname helper -- all of which grant the CA private key while the
# guard stays green.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/nats-rd/cozyrds/nats.yaml"

# The complete set of grants the tenant holds, asserted as one value rather than
# per-dimension: a name whitelist alone passes an added label selector, and a
# label whitelist alone passes an added name. Either dimension can reach a
# key-bearing Secret -- controller.cert-manager.io/fao is stamped on both of
# them, so a selector on it is a one-line grant of the CA private key.
EXPECTED_RD_INCLUDE='[{"resourceNames":["nats-{{ .name }}-credentials"]},{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}]'

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
