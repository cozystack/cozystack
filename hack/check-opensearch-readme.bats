#!/usr/bin/env bats
# Tests that the opensearch README.md has the correct structure after generation.
#
# These are static checks on the committed README.md — they do not invoke
# make generate (which requires cozyvalues-gen in PATH). They verify that:
#   1. tls.enabled description is not truncated (ends with "off otherwise")
#   2. topologySpreadPolicy appears outside the TLS section
#   3. opensearch-rd cozyrds labels the tenant CA and no server certificate

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

@test "cozyrds opensearch.yaml labels the tenant CA for the tenant registry" {
  # The key-free trust anchor the CA-extraction controller publishes, selected by
  # label rather than by name. This entry drives the lineage webhook tenant-resource
  # label only — direct read is a separate grant in the chart Role, which
  # tests/dashboard-resourcemap_test.yaml covers.
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
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }

  credentials='{"resourceNames":["opensearch-{{ .name }}-credentials"]}'
  tenant_ca='{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}'

  entries="$(yq --output-format=json -I=0 '.spec.secrets.include[]' "$COZYRDS")"

  # Guard against a vacuous pass. A loop over nothing is a green test, and this one
  # guards tenant read access to a Secret holding the HTTP CA private key — so assert
  # the list is the size we think it is before concluding anything about its members.
  # Without this the test would still report ok if the path were restructured away.
  # `|| true` because grep exits non-zero on no match, which under set -e would kill
  # the test before the diagnostic below could explain what was expected.
  count="$(printf '%s\n' "$entries" | grep -c '^{' || true)"
  if [ "$count" -ne 2 ]; then
      echo "expected exactly 2 secrets.include entries, found $count" >&2
      printf '%s\n' "$entries" >&2
      return 1
  fi

  while IFS= read -r entry; do
      [ -n "$entry" ] || continue
      if [ "$entry" != "$credentials" ] && [ "$entry" != "$tenant_ca" ]; then
          echo "secrets.include has an entry that is neither the credentials Secret" >&2
          echo "nor the key-free tenant-ca projection: $entry" >&2
          return 1
      fi
  done <<EOF
$entries
EOF
}

@test "cozyrds opensearch.yaml selects the projection, never a publish source" {
  # The selector must name the key-free projection (tenant-ca), not the label that
  # marks a publish SOURCE (publish-ca-cert). Sources are key-bearing: the chart puts
  # publish-ca-cert on the cert-manager CA Secret, which holds the CA private key.
  # Selecting by that label instead would hand the tenant the key directly.
  ! grep -q "internal.cozystack.io/publish-ca-cert" "$COZYRDS"
}
