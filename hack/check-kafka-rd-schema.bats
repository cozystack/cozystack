#!/usr/bin/env bats
# Unit test: kafka-rd cozyrds embedded openAPISchema must declare
# tls.enabled.type as the scalar string "boolean". The array form
# ["boolean","null"] breaks the ApplicationDefinition CRD's own unmarshal of
# the embedded schema; an unset value is represented by the field being
# absent (omitempty *bool) rather than an explicit null, and the template
# detects that via `kindIs "invalid"`.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/kafka-rd/cozyrds/kafka.yaml"

@test "kafka-rd cozyrds embedded schema declares tls.enabled type as scalar boolean" {
  # The openAPISchema value is a single-line JSON string after "openAPISchema: |-"
  SCHEMA_JSON="$(grep -A1 'openAPISchema: |-' "$COZYRDS" | tail -n1 | sed 's/^[[:space:]]*//')"
  [ -n "$SCHEMA_JSON" ] || { echo "Could not extract openAPISchema from $COZYRDS" >&2; exit 1; }
  printf '%s' "$SCHEMA_JSON" | jq -e '.properties.tls.properties.enabled.type == "boolean"'
}
