#!/usr/bin/env bats
# Unit test: mariadb-rd cozyrds must publish the trust anchor from the
# operator's key-free CA bundle and must expose it to the tenant through a
# specific label selector. The chart's own helm-unittest suite cannot cover
# this file, so the invariants that decide whether a private key can reach a
# tenant are pinned here.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/mariadb-rd/cozyrds/mariadb.yaml"

@test "mariadb-rd declares the CA source as the operator ca-bundle" {
  grep -q 'sourceSecretName: "{{ .release }}-ca-bundle"' "$COZYRDS"
}

@test "mariadb-rd extracts ca.crt from the declared source" {
  grep -q "sourceKey: ca.crt" "$COZYRDS"
}

# -ca-tls and -tls hold the CA and server private keys. Declaring either as the
# publication source would run the extraction next to private key material.
@test "mariadb-rd never names a key-bearing Secret as the CA source" {
  if grep -E "sourceSecretName:.*-(ca-)?tls\"" "$COZYRDS"; then
    echo "CA source points at a key-bearing Secret" >&2
    exit 1
  fi
}

@test "mariadb-rd selects the tenant CA projection by label" {
  grep -q "internal.cozystack.io/tenant-ca: \"true\"" "$COZYRDS"
}

# An empty matchLabels compiles to a match-everything selector, which would
# project every Secret in the namespace — credentials included — to the tenant.
@test "mariadb-rd has no empty matchLabels selector" {
  if grep -qE "matchLabels:\s*\{\s*\}" "$COZYRDS"; then
    echo "Found an empty matchLabels selector (matches every Secret)" >&2
    exit 1
  fi
}

@test "mariadb-rd does not expose key-bearing TLS Secrets by name" {
  if grep -E "^\s+- mariadb-\{\{ \.name \}\}-(ca-)?tls\s*$" "$COZYRDS"; then
    echo "Found a key-bearing TLS Secret in the tenant include list" >&2
    exit 1
  fi
}
