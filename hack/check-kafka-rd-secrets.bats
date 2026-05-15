#!/usr/bin/env bats
# Unit test: kafka-rd cozyrds must reference the Strimzi-generated
# clients-ca-cert secret (not the bare clients-ca name, which does not exist).

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/kafka-rd/cozyrds/kafka.yaml"

@test "kafka-rd cozyrds references clients-ca-cert (Strimzi actual name)" {
  grep -q "clients-ca-cert" "$COZYRDS"
}

@test "kafka-rd cozyrds does not reference bare clients-ca (wrong name)" {
  if grep -qP "clients-ca(?!-)" "$COZYRDS"; then
    echo "Found bare 'clients-ca' reference (missing '-cert' suffix)" >&2
    exit 1
  fi
}
