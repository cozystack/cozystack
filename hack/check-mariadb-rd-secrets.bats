#!/usr/bin/env bats
# Unit test: mariadb-rd cozyrds. The chart's helm-unittest suite cannot reach
# this file, so its Secret-exposure invariants are pinned here.
#
# The tenant reaches the TLS trust anchor as a key-free projection, never by
# name. The chart renders a TenantProjection sentinel naming the operator's
# key-free <release>-ca-bundle as the extraction source
# (packages/apps/mariadb/templates/tenant-projection.yaml); the CA-extraction
# controller publishes the result as "<release>.tenant-ca" carrying
# internal.cozystack.io/tenant-ca; and this ApplicationDefinition selects that
# label. The sentinel's own fields belong to the chart and are pinned by
# packages/apps/mariadb/tests/tenant_projection_test.yaml, which compares
# sourceSecretName against an exact key-free name — a stronger guard than any
# grep here could be, and one this file cannot make, since it never sees the
# chart.
#
# What is left for this file is the grant surface: that secrets.include is
# exactly the credentials Secret plus the tenant-ca selector, that no selector
# degrades to match-everything, and that every key-bearing and credential Secret
# is named in secrets.exclude, which wins over include.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/mariadb-rd/cozyrds/mariadb.yaml"

# The complete set of grants the tenant holds, asserted as one value rather than
# per-dimension: a name whitelist alone passes an added label selector, and a
# label whitelist alone passes an added name. Either dimension reaches a
# key-bearing Secret — controller.cert-manager.io/fao is stamped on both
# -ca-tls and -tls, so a selector on it is a one-line grant of the CA private
# key.
EXPECTED_RD_INCLUDE='[{"resourceNames":["mariadb-{{ .name }}-credentials"]},{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}]'

# Subsumed by the whole-value comparison below, which cannot match without this
# entry. Kept because the runner stops at the first failing test, so this one
# fires first and says what the loss costs rather than printing two JSON blobs
# to diff.
@test "mariadb-rd selects the key-free tenant-CA projection by label" {
  count=$(yq eval \
    '[.spec.secrets.include[] | select(.matchLabels."internal.cozystack.io/tenant-ca" == "true")] | length' \
    "$COZYRDS") || exit 1

  if [ "$count" -lt 1 ]; then
    echo 'mariadb-rd does not select internal.cozystack.io/tenant-ca: "true"' >&2
    echo "Without it the tenant cannot read ca.crt and TLS verification is impossible." >&2
    exit 1
  fi
}

@test "mariadb-rd grants nothing beyond the credentials Secret and the trust anchor" {
  include=$(yq eval --output-format=json --indent=0 '.spec.secrets.include' "$COZYRDS") || exit 1

  if [ "$include" != "$EXPECTED_RD_INCLUDE" ]; then
    echo "Unexpected mariadb-rd spec.secrets.include: $include" >&2
    echo "Expected exactly: $EXPECTED_RD_INCLUDE" >&2
    echo "mariadb-<name>-ca-tls holds the CA private key and mariadb-<name>-tls the" >&2
    echo "server key; the operator's own -ca and -client-cert hold keys too." >&2
    echo "None of them may be granted to a tenant, by name or by label." >&2
    exit 1
  fi
}

# An empty matchLabels compiles to a match-everything selector. The lineage
# webhook only evaluates it for objects whose ownership resolves to this
# instance, so the blast radius is the instance's own Secrets rather than the
# whole namespace — but that set includes -ca-tls and -tls, the key-bearing
# pair this design keeps away from the tenant. Both spellings are empty: the
# inline "matchLabels: {}" and a bare "matchLabels:" with nothing nested.
#
# Scanned over the whole file rather than over secrets.include alone, so a
# catch-all introduced under services: is caught by the same pass.
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

# Extracts the exclude block alone. The two lists sit at the same indentation,
# so an unscoped grep is satisfied by an entry in either one — which would let a
# name be MOVED from exclude to include, the single most dangerous edit here,
# without any guard noticing.
excluded_block() {
  awk '/^  secrets:/{sec=1; ex=0; next}
       /^  [a-z]/{sec=0; ex=0}
       sec && /^    exclude:/{ex=1; next}
       sec && /^    [a-z]/{ex=0}
       sec && ex' "$COZYRDS"
}

# The names below only constrain anything while they sit under resourceNames:
# matchName returns true for every name when resourceNames is nil, so an exclude
# entry that lost that key would match everything and void the backstop while
# each name was still present in the block.
@test "mariadb-rd scopes the exclude entries by name" {
  excluded_block | grep -q "^      - resourceNames:\$"
}

# Exclude backstop. The include selector matches by label with no name
# constraint, so these names are the enumerable part of the gap: exclude wins
# over include in matchResourceToExcludeInclude, so a Secret named here cannot
# be promoted even if it acquires the label.
@test "mariadb-rd excludes every key-bearing Secret" {
  block="$(excluded_block)"
  for n in ca-tls tls ca server-cert client-cert; do
    echo "$block" | grep -q "^          - mariadb-{{ .name }}-$n\$" || {
      echo "key-bearing Secret -$n missing from exclude" >&2
      exit 1
    }
  done
}

@test "mariadb-rd excludes internal credentials and backup keys" {
  block="$(excluded_block)"
  for n in root password repl-password metrics-password metrics-config backup regsecret; do
    echo "$block" | grep -q "^          - mariadb-{{ .name }}-$n\$" || {
      echo "credential Secret -$n missing from exclude" >&2
      exit 1
    }
  done
}

# The backstop must not swallow what the tenant is supposed to receive: exclude
# is evaluated before include and wins outright, so it sits upstream of both
# grants. Every other line in that block is about withholding a Secret, which
# makes adding one of these to it a plausible edit — and one that would revoke
# the tenant's connection string, or its trust anchor, in silence. The
# projection is the dotted "<release>.tenant-ca" the CA-extraction controller
# writes, not the operator's own -ca-bundle, which the tenant never reads by
# name under this design.
@test "mariadb-rd does not exclude the Secrets the tenant is meant to read" {
  for name in "mariadb-{{ .name }}-credentials" "mariadb-{{ .name }}.tenant-ca"; do
    hits="$(yq "[.spec.secrets.exclude[].resourceNames[]? | select(. == \"$name\")] | length" "$COZYRDS")" || exit 1
    if [ "$hits" != "0" ]; then
      echo "exclude names $name, which the tenant is meant to read" >&2
      exit 1
    fi
  done
}
