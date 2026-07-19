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
# with a fully green chart suite: helm-unittest covers the chart templates, and
# neither the ResourceDefinition nor the dashboard Role is a chart template.
#
# The grants are asserted as WHITELISTS against rendered output rather than as
# a blacklist of forbidden names over source text. A blacklist grepping for
# "{{ .Release.Name }}-ca" is defeated by quoting the scalar, by $.Release.Name,
# or by a fullname helper -- all of which grant the CA private key while the
# guard stays green.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/nats-rd/cozyrds/nats.yaml"
CHART="$REPO_ROOT/packages/apps/nats"

# Secret names the tenant is allowed to hold by NAME. Credentials only: the
# trust anchor is reached by label, through the tenantsecrets API.
EXPECTED_RD_NAMES='nats-{{ .name }}-credentials'
EXPECTED_ROLE_NAMES='nats-test-credentials'

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

@test "nats-rd cozyrds grants no Secret by name except the credentials Secret" {
  if [ ! -f "$COZYRDS" ]; then
    echo "ResourceDefinition not found at $COZYRDS -- did it move?" >&2
    exit 1
  fi

  names=$(yq eval \
    '[.spec.secrets.include[].resourceNames // [] | .[]] | sort | join(",")' \
    "$COZYRDS") || exit 1

  if [ "$names" != "$EXPECTED_RD_NAMES" ]; then
    echo "Unexpected resourceNames in nats-rd spec.secrets.include: $names" >&2
    echo "Expected exactly: $EXPECTED_RD_NAMES" >&2
    echo "nats-<name>-ca holds the CA private key and nats-<name>-tls the server key;" >&2
    echo "neither may be granted to a tenant." >&2
    exit 1
  fi
}

# Asserted against RENDERED output, so the spelling used in the template --
# quoted, helper-derived, or otherwise -- cannot smuggle a name past the check.
@test "nats dashboard Role grants no Secret by name except the credentials Secret" {
  if [ ! -d "$CHART" ]; then
    echo "Chart not found at $CHART -- did it move?" >&2
    exit 1
  fi

  rendered=$(helm template nats-test "$CHART" \
    --namespace tenant-test \
    --set '_cluster.cluster-domain=cozy.local' \
    --show-only templates/dashboard-resourcemap.yaml 2>/dev/null) || {
    echo "helm template failed for $CHART" >&2
    exit 1
  }

  names=$(printf '%s\n' "$rendered" | yq eval \
    '[select(.kind == "Role") | .rules[] | select(.resources[] == "secrets") | .resourceNames[]] | sort | join(",")' \
    -) || exit 1

  # Fail closed: an empty result means the Role stopped granting Secrets at all,
  # or the render silently produced nothing. Either way this guard is no longer
  # observing what it claims to observe.
  if [ -z "$names" ]; then
    echo "No secrets grant found in the rendered dashboard Role -- guard is blind." >&2
    exit 1
  fi

  if [ "$names" != "$EXPECTED_ROLE_NAMES" ]; then
    echo "Unexpected Secret grants in the nats dashboard Role: $names" >&2
    echo "Expected exactly: $EXPECTED_ROLE_NAMES" >&2
    echo "nats-<release>-ca holds the CA PRIVATE KEY and nats-<release>-tls the" >&2
    echo "server private key. The trust anchor reaches the tenant as the key-free" >&2
    echo "<release>.tenant-ca projection via tenantsecrets, not by a name grant." >&2
    exit 1
  fi
}
