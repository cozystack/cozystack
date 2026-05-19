#!/usr/bin/env bats
# Unit test: kafka-rd cozyrds embedded openAPISchema must declare
# tls.enabled as nullable (["boolean","null"]) so the ApplicationDefinition
# CRD validation does not reject an unset (null) value.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/kafka-rd/cozyrds/kafka.yaml"

@test "kafka-rd cozyrds embedded schema has tls.enabled type as [boolean, null]" {
  # The openAPISchema value is a single-line JSON string after "openAPISchema: |-"
  SCHEMA_JSON="$(grep -A1 'openAPISchema: |-' "$COZYRDS" | tail -n1 | sed 's/^[[:space:]]*//')"
  [ -n "$SCHEMA_JSON" ] || { echo "Could not extract openAPISchema from $COZYRDS" >&2; exit 1; }
  echo "$SCHEMA_JSON" | jq -e '.properties.tls.properties.enabled.type == ["boolean", "null"]'
}
