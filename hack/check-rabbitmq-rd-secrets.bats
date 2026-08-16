#!/usr/bin/env bats
# Unit test: rabbitmq-rd cozyrds decides which release Secrets a tenant can read.
# The chart's own helm-unittest suite cannot cover this file, so the invariants
# that decide whether a private key can reach a tenant are pinned here.
#
# rabbitmq resolves on the label-driven leg: cluster-operator produces no CA
# object of its own (spec.tls.caSecretName is an input it only consumes), so the
# chart mints the CA through cert-manager and labels it for publication. There is
# therefore no sourceSecretName to assert — the include selector is the contract.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/rabbitmq-rd/cozyrds/rabbitmq.yaml"

@test "rabbitmq-rd includes the trust anchor by the engine-agnostic label" {
  grep -q 'internal.cozystack.io/tenant-ca: "true"' "$COZYRDS"
}

# A bare `matchLabels: {}` selects labels.Everything(), promoting every Secret in
# the namespace. The selector must stay non-empty and specific.
@test "rabbitmq-rd never uses an empty label selector" {
  if grep -Eq 'matchLabels: *\{\}' "$COZYRDS"; then
    echo "empty matchLabels selects every Secret in the namespace" >&2
    exit 1
  fi
}

# The leaf holds the server private key and the CA holds the CA private key.
# Either one included by name would hand a tenant a key alongside the ca.crt it
# legitimately wants.
@test "rabbitmq-rd does not include key-bearing TLS Secrets by name" {
  include_block="$(awk '/^  secrets:/,/^  services:/' "$COZYRDS" | awk '/^    include:/,0')"
  if echo "$include_block" | grep -Eq '^ *- *rabbitmq-\{\{ \.name \}\}-(ca|tls)$'; then
    echo "a key-bearing TLS Secret is included by name" >&2
    exit 1
  fi
}

# exclude is evaluated before include, so it is the structural backstop against a
# mis-stamped trust-anchor label on a key-bearing object.
@test "rabbitmq-rd excludes the cert-manager chain" {
  exclude_block="$(awk '/^    exclude:/,/^    include:/' "$COZYRDS")"
  echo "$exclude_block" | grep -q 'rabbitmq-{{ .name }}-ca'
  echo "$exclude_block" | grep -q 'rabbitmq-{{ .name }}-tls'
}

# The Erlang cookie is the distribution shared secret: holding it and reaching
# port 25672 is enough to join the cluster as a peer node and run arbitrary
# Erlang. disableNonTLSListeners does not protect that port.
@test "rabbitmq-rd excludes the erlang cookie" {
  exclude_block="$(awk '/^    exclude:/,/^    include:/' "$COZYRDS")"
  echo "$exclude_block" | grep -q 'rabbitmq-{{ .name }}-erlang-cookie'
}

# The default-user Secret carries the broker credentials the tenant is meant to
# have. Excluding it would break the resource map.
@test "rabbitmq-rd still includes the default-user Secret" {
  include_block="$(awk '/^    include:/,/^  services:/' "$COZYRDS")"
  echo "$include_block" | grep -q 'rabbitmq-{{ .name }}-default-user'
}
