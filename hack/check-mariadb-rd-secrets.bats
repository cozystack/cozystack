#!/usr/bin/env bats
# Unit test: mariadb-rd cozyrds. The chart's helm-unittest suite cannot reach
# this file, so its Secret-exposure invariants are pinned here.
#
# Read the coverage honestly. Two of these tests are live: the bundle is exposed
# by name and no key-bearing Secret is listed, both against secrets.include,
# which the API accepts today. The empty-selector guards are live too. The three
# touching caCert and the tenant-ca label are placeholders — that field is not
# in ApplicationDefinitionSpec yet and nothing writes that label, so they check
# that the block we intend to ship is present and spelled consistently, not that
# it does anything. If the API lands with different names they will need
# updating; they cannot detect that on their own.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/mariadb-rd/cozyrds/mariadb.yaml"

# Placeholder (see header): pins the intended spelling, not a live behaviour.
@test "mariadb-rd declares the CA source as the operator ca-bundle" {
  grep -q 'sourceSecretName: "mariadb-{{ .name }}-ca-bundle"' "$COZYRDS"
}

# The webhook builds the only context these templates get, and it holds kind,
# name and namespace — there is no release key. A template that reads one
# renders empty, so the name would resolve to a Secret that does not exist.
# The release prefix is a literal, exactly as the exclude entry below spells it.
@test "mariadb-rd never templates an undefined key into a Secret name" {
  ! grep -q '{{ \.release }}' "$COZYRDS"
}

@test "mariadb-rd extracts ca.crt from the declared source" {
  grep -q "sourceKey: ca.crt" "$COZYRDS"
}

# -ca-tls and -tls hold the CA and server private keys. Declaring either as the
# publication source would run the extraction next to private key material.
# Placeholder (see header): the declaration is inert, but keeping a key-bearing
# name out of it means the block is already correct when the API arrives.
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

# Placeholder (see header): nothing writes this label yet.
@test "mariadb-rd selects the tenant CA projection by label" {
  # Must appear inside a matchLabels selector, not merely somewhere in the file:
  # the label only exposes the projection when it is what the selector matches on.
  grep -A2 "matchLabels:" "$COZYRDS" | grep -q "internal.cozystack.io/tenant-ca: \"true\""
}

# An empty matchLabels compiles to a match-everything selector. The lineage
# webhook only evaluates it for objects whose ownership resolves to this
# instance, so the blast radius is the instance's own Secrets rather than the
# whole namespace — but that set includes -ca-tls and -tls, the key-bearing
# pair this design keeps away from the tenant. Both spellings are empty: the
# inline "matchLabels: {}" and a bare "matchLabels:" with nothing nested.
@test "mariadb-rd has no empty matchLabels selector" {
  if grep -qE "matchLabels:[[:space:]]*\{[[:space:]]*\}" "$COZYRDS"; then
    echo "Found an inline empty matchLabels selector (matches every Secret the instance owns)" >&2
    exit 1
  fi
  # A bare matchLabels: must be followed by a more-indented "key: value" line.
  awk '
    /matchLabels:[[:space:]]*$/ {
      match($0, /^[[:space:]]*/); indent = RLENGTH
      if ((getline nextline) <= 0) { print "matchLabels: at end of file"; exit 1 }
      match(nextline, /^[[:space:]]*/)
      if (RLENGTH <= indent || nextline !~ /:/) {
        print "Found a bare matchLabels: with no labels under it (matches every Secret the instance owns)"
        exit 1
      }
    }
  ' "$COZYRDS"
}

@test "mariadb-rd does not expose key-bearing TLS Secrets by name" {
  # Scoped to secrets.include — a -tls entry under services: is a Service name,
  # and under secrets.exclude it is the backstop doing its job; neither must
  # trip this. Brace spacing is not pinned, the entry may be quoted, and a
  # trailing comment must not hide it.
  awk '/^  secrets:/{sec=1; inc=0; next}
       /^  [a-z]/{sec=0; inc=0}
       sec && /^    include:/{inc=1; next}
       sec && /^    [a-z]/{inc=0}
       sec && inc' "$COZYRDS" \
    | sed 's/#.*$//' \
    | grep -E "^[[:space:]]*-[[:space:]].*-(ca-)?tls\"?[[:space:]]*$" && {
        echo "Found a key-bearing TLS Secret in the tenant include list" >&2
        exit 1
      }
  return 0
}

# Exclude backstop. The include selector matches by label with no name
# constraint, so these names are the enumerable part of the gap: exclude wins
# over include in matchResourceToExcludeInclude, so a Secret named here cannot
# be promoted even if it acquires the label.
@test "mariadb-rd excludes every key-bearing Secret" {
  for n in ca-tls tls ca server-cert client-cert; do
    grep -q "^          - mariadb-{{ .name }}-$n\$" "$COZYRDS" || {
      echo "key-bearing Secret -$n missing from exclude" >&2
      exit 1
    }
  done
}

@test "mariadb-rd excludes internal credentials and backup keys" {
  for n in root password repl-password metrics-password metrics-config backup regsecret; do
    grep -q "^          - mariadb-{{ .name }}-$n\$" "$COZYRDS" || {
      echo "credential Secret -$n missing from exclude" >&2
      exit 1
    }
  done
}

# The backstop must not swallow what the tenant is supposed to receive: exclude
# beats include, so an over-broad entry here silently removes the trust anchor.
@test "mariadb-rd does not exclude the Secrets it exposes" {
  excluded=$(awk '/^  secrets:/{sec=1; ex=0; next}
                  /^  [a-z]/{sec=0; ex=0}
                  sec && /^    exclude:/{ex=1; next}
                  sec && /^    [a-z]/{ex=0}
                  sec && ex' "$COZYRDS")
  for n in credentials ca-bundle; do
    if echo "$excluded" | grep -q -- "-$n\$"; then
      echo "exclude list contains -$n, which the tenant is meant to read" >&2
      exit 1
    fi
  done
}
