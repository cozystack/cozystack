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
  # Quote-agnostic: an unquoted value is just as wrong as a quoted one.
  if grep -E "sourceSecretName:.*-(ca-)?tls[\"[:space:]]*$" "$COZYRDS"; then
    echo "CA source points at a key-bearing Secret" >&2
    exit 1
  fi
}

# The RBAC grant in dashboard-resourcemap.yaml and this list have to agree:
# the Role decides whether the tenant may read the bundle, this decides whether
# it is surfaced as a tenant resource at all.
@test "mariadb-rd exposes the operator CA bundle by name" {
  grep -q "mariadb-{{ .name }}-ca-bundle" "$COZYRDS"
}

@test "mariadb-rd selects the tenant CA projection by label" {
  # Must appear inside a matchLabels selector, not merely somewhere in the file:
  # the label only exposes the projection when it is what the selector matches on.
  grep -A2 "matchLabels:" "$COZYRDS" | grep -q "internal.cozystack.io/tenant-ca: \"true\""
}

# An empty matchLabels compiles to a match-everything selector, which would
# project every Secret in the namespace — credentials included — to the tenant.
# Both spellings are empty: the inline "matchLabels: {}" and a bare
# "matchLabels:" with nothing nested under it.
@test "mariadb-rd has no empty matchLabels selector" {
  if grep -qE "matchLabels:[[:space:]]*\{[[:space:]]*\}" "$COZYRDS"; then
    echo "Found an inline empty matchLabels selector (matches every Secret)" >&2
    exit 1
  fi
  # A bare matchLabels: must be followed by a more-indented "key: value" line.
  awk '
    /matchLabels:[[:space:]]*$/ {
      match($0, /^[[:space:]]*/); indent = RLENGTH
      if ((getline nextline) <= 0) { print "matchLabels: at end of file"; exit 1 }
      match(nextline, /^[[:space:]]*/)
      if (RLENGTH <= indent || nextline !~ /:/) {
        print "Found a bare matchLabels: with no labels under it (matches every Secret)"
        exit 1
      }
    }
  ' "$COZYRDS"
}

@test "mariadb-rd does not expose key-bearing TLS Secrets by name" {
  # Scoped to the secrets: block — a -tls entry under services: is a Service
  # name, not a Secret, and must not trip this. Brace spacing is not pinned, the
  # entry may be quoted, and a trailing comment must not hide it.
  awk '/^  secrets:/{inblock=1; next} /^  [a-z]/{inblock=0} inblock' "$COZYRDS" \
    | sed 's/#.*$//' \
    | grep -E "^[[:space:]]*-[[:space:]].*-(ca-)?tls\"?[[:space:]]*$" && {
        echo "Found a key-bearing TLS Secret in the tenant include list" >&2
        exit 1
      }
  return 0
}
