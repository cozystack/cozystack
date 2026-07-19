#!/usr/bin/env bats
# Tests that the opensearch README.md has the correct structure after generation.
#
# These are static checks on the committed README.md — they do not invoke
# make generate (which requires cozyvalues-gen in PATH). They verify that:
#   1. tls.enabled description is not truncated (ends with "off otherwise")
#   2. topologySpreadPolicy appears outside the TLS section
#   3. opensearch-rd cozyrds exposes the tenant CA and no server certificate

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
README="$REPO_ROOT/packages/apps/opensearch/README.md"
COZYRDS="$REPO_ROOT/packages/system/opensearch-rd/cozyrds/opensearch.yaml"

@test "tls.enabled description is not truncated" {
  grep -q "off otherwise" "$README"
}

@test "topologySpreadPolicy is not in TLS configuration section" {
  # TLS section should contain only tls/tls.enabled rows, not topology spread.
  # The awk pattern skips the heading line itself, then prints until the next
  # section heading so the range is non-vacuous.
  ! awk '/### TLS configuration/{found=1; next} found && /^### /{exit} found' "$README" | grep -q "topologySpreadPolicy"
}

@test "topologySpreadPolicy appears in README outside TLS section" {
  grep -q "topologySpreadPolicy" "$README"
}

@test "cozyrds opensearch.yaml exposes the tenant CA to the tenant" {
  # The key-free trust anchor the CA-extraction controller publishes. Selected by
  # label rather than by name: the chart mints the HTTP CA through cert-manager
  # and labels that Secret for publication, so opensearch resolves on the
  # label-driven leg.
  grep -q "internal.cozystack.io/tenant-ca" "$COZYRDS"
}

@test "cozyrds opensearch.yaml exposes no server certificate to the tenant" {
  # A tenant needs ca.crt to verify the endpoint and nothing more. Both the leaf
  # Secret and the cert-manager CA Secret carry a private key under tls.key, so
  # neither may be listed here — only the controller's key-free projection.
  #
  # Asserted on the SHAPE of secrets.include, not by grepping one spelling: this is
  # the only thing standing between the CA private key and the tenant, and a name
  # written a different way, or a selector that happens to match a key-bearing
  # Secret, has to fail this too. Every entry must be one of exactly two known-safe
  # forms — the credentials Secret by name, or the tenant-ca projection by label.
  credentials='{"resourceNames":["opensearch-{{ .name }}-credentials"]}'
  tenant_ca='{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}'

  while IFS= read -r entry; do
      [ -n "$entry" ] || continue
      if [ "$entry" != "$credentials" ] && [ "$entry" != "$tenant_ca" ]; then
          echo "secrets.include has an entry that is neither the credentials Secret" >&2
          echo "nor the key-free tenant-ca projection: $entry" >&2
          return 1
      fi
  done <<EOF
$(yq --output-format=json -I=0 '.spec.secrets.include[]' "$COZYRDS")
EOF
}

@test "cozyrds opensearch.yaml selects the projection, never a publish source" {
  # The selector must name the key-free projection (tenant-ca), not the label that
  # marks a publish SOURCE (publish-ca-cert). Sources are key-bearing: the chart puts
  # publish-ca-cert on the cert-manager CA Secret, which holds the CA private key.
  # Selecting by that label instead would hand the tenant the key directly.
  ! grep -q "internal.cozystack.io/publish-ca-cert" "$COZYRDS"
}
