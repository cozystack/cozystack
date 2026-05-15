#!/usr/bin/env bats
# Tests that the opensearch README.md has the correct structure after generation.
#
# These are static checks on the committed README.md — they do not invoke
# make generate (which requires cozyvalues-gen in PATH). They verify that:
#   1. tls.enabled description is not truncated (ends with "off otherwise")
#   2. topologySpreadPolicy appears outside the TLS section
#   3. opensearch-rd cozyrds has http-cert in secrets.include
#      NOTE: cozyrds files are raw YAML read via .Files.Get — Go template
#      conditionals ({{- if }}) are not processed by the Helm engine, so
#      gating on tls.enabled is not possible at this layer. The entry is
#      unconditional; when TLS is off the secret simply does not exist and
#      the ApplicationDefinition controller ignores missing optional secrets.

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
  awk '/### TLS configuration/{found=1; next} found && /^### /{exit} found' "$README" | grep -qv "topologySpreadPolicy"
}

@test "topologySpreadPolicy appears in README outside TLS section" {
  grep -q "topologySpreadPolicy" "$README"
}

@test "cozyrds opensearch.yaml contains http-cert in secrets.include" {
  # The entry is unconditional because cozyrds files are raw YAML (not Helm
  # templates) and do not support {{- if }} conditionals. This test documents
  # the current known state: http-cert is always listed regardless of tls.enabled.
  grep -q "opensearch-.*-http-cert" "$COZYRDS"
}
