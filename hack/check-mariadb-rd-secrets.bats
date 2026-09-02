#!/usr/bin/env bats
# Unit test: mariadb-rd's tenant grant surface. The chart's helm-unittest suite
# cannot reach this package, so its Secret-exposure invariants are pinned here.
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
# Asserted against the RENDERED chart, not against cozyrds/mariadb.yaml on
# disk, following hack/check-qdrant-rd-secrets.bats. templates/cozyrd.yaml
# globs cozyrds/* and emits each match through .Files.Get, so what ships is the
# render: reading one source file would miss a second file in that directory,
# and a dotfile the glob picks up. Helm ignores dotfiles only under templates/,
# so one here does ship.
#
# spec.secrets and spec.services are each compared WHOLE rather than probed for
# known-bad shapes. Probing is what a name-shaped check cannot see:
#
#   - an exclude entry that selects by LABEL instead of by name. matchName
#     returns true for every name when resourceNames is nil
#     (internal/lineagecontrollerwebhook/matcher.go), and exclude wins over
#     include, so one such entry withholds everything — the tenant's
#     connection string and its trust anchor included. A pass reading
#     .exclude[].resourceNames[]? cannot see an entry that has no
#     resourceNames at all.
#   - an entry that restricts nothing, spelled as an empty matchLabels, an
#     empty matchExpressions, or a bare {}. Only the first is a literal anyone
#     greps for; ApplicationDefinitionResourceSelector inlines
#     metav1.LabelSelector, so all three are legal.
#   - a SECOND matchLabels include selecting a label the key-bearing Secrets
#     also carry. cert-manager stamps controller.cert-manager.io/fao on both
#     -ca-tls and -tls, so one such entry grants the CA private key while every
#     name in the file stays correct.
#
# Order within each list is meaningless — the matcher ORs across entries — so
# both are sorted before comparing, and an absent list is normalized to the
# empty list it means. The sort is jq's, which orders objects; yq's sort would
# leave a list of maps as it found it.
#
# Every document count uses `yq eval-all`, never `yq eval`: `eval` runs the
# expression once PER DOCUMENT, so `[.] | length` reports 1 per document rather
# than the number of them. Emptiness is checked separately, since yq
# synthesizes a null document for empty input.
#
# Everything is spelled inline rather than through a shell helper:
# hack/cozytest.sh inserts `return 0` before any line that is exactly `}`, so a
# helper whose own last command decides the outcome returns 0 regardless.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/system/mariadb-rd"

NORMALIZE='{exclude: (.exclude // [] | sort), include: (.include // [] | sort)}'

# The complete tenant-visible Secret surface: the credentials Secret by name,
# the trust anchor by label, every key-bearing and credential Secret withheld
# by name. mariadb-<name>-ca-tls holds the CA private key and mariadb-<name>-tls
# the server key; the operator's own -ca, -server-cert and -client-cert hold
# keys too. None of them may reach a tenant, by name or by a label they carry.
EXPECTED_SECRETS='{"exclude":[{"resourceNames":["mariadb-{{ .name }}-ca-tls","mariadb-{{ .name }}-tls","mariadb-{{ .name }}-ca","mariadb-{{ .name }}-server-cert","mariadb-{{ .name }}-client-cert","mariadb-{{ .name }}-root","mariadb-{{ .name }}-password","mariadb-{{ .name }}-repl-password","mariadb-{{ .name }}-metrics-password","mariadb-{{ .name }}-metrics-config","mariadb-{{ .name }}-backup","mariadb-{{ .name }}-regsecret"]}],"include":[{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}},{"resourceNames":["mariadb-{{ .name }}-credentials"]}]}'

# The Services an instance exposes, pinned for the same reason: a catch-all
# selector reaches this list too, and nothing else in the tree holds it.
EXPECTED_SERVICES='{"exclude":[],"include":[{"resourceNames":["mariadb-{{ .name }}","mariadb-{{ .name }}-primary","mariadb-{{ .name }}-secondary"]}]}'

@test "mariadb-rd renders exactly one ApplicationDefinition" {
  # stderr is deliberately not discarded: when the render breaks, helm's own
  # message is the only thing that says why.
  rendered=$(helm template mariadb-rd "$CHART") || return 1

  # Fail closed: an empty render makes every assertion below vacuous, and yq
  # cannot distinguish it from one document.
  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  count=$(printf '%s\n' "$rendered" | yq eval-all '[.] | length' -) || return 1

  if [ "$count" != "1" ]; then
    echo "Expected exactly 1 rendered document from $CHART, got: $count" >&2
    echo "templates/cozyrd.yaml globs cozyrds/*, so every file there -- including" >&2
    echo "a dotfile -- ships its own tenant grants. Extend this suite before" >&2
    echo "adding one." >&2
    return 1
  fi
}

# Subsumed by the whole-value comparison below, which cannot match without this
# entry. Kept because the runner stops at the first failing test, so this one
# fires first and says what the loss costs rather than printing two JSON blobs
# to diff.
@test "mariadb-rd selects the key-free tenant-CA projection by label" {
  rendered=$(helm template mariadb-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  count=$(printf '%s\n' "$rendered" | yq eval-all \
    '[.. | select(has("secrets")) | .secrets.include[] | select(.matchLabels."internal.cozystack.io/tenant-ca" == "true")] | length' \
    -) || return 1

  if [ "$count" -lt 1 ]; then
    echo 'mariadb-rd does not select internal.cozystack.io/tenant-ca: "true"' >&2
    echo "Without it the tenant cannot read ca.crt and TLS verification is impossible." >&2
    return 1
  fi
}

# Also subsumed, and kept for the same reason: a dropped name is the likeliest
# single edit here, and this names which one went rather than leaving it to a
# diff of two twelve-element lists.
@test "mariadb-rd withholds every key-bearing and credential Secret by name" {
  rendered=$(helm template mariadb-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  for n in ca-tls tls ca server-cert client-cert \
           root password repl-password metrics-password metrics-config \
           backup regsecret; do
    hits=$(printf '%s\n' "$rendered" | yq eval-all \
      "[.. | select(has(\"secrets\")) | .secrets.exclude[].resourceNames[]? | select(. == \"mariadb-{{ .name }}-$n\")] | length" \
      -) || return 1
    if [ "$hits" -lt 1 ]; then
      echo "mariadb-<name>-$n is missing from secrets.exclude" >&2
      echo "The include selector matches by label with no name constraint, so this" >&2
      echo "list is the enumerable part of that gap; exclude wins over include." >&2
      return 1
    fi
  done
}

# Whole-structure comparison. Anything added to the tenant's Secret surface --
# by name, by label, or by an unrestricted entry -- changes this string, and so
# does anything removed from the backstop.
@test "mariadb-rd grants exactly the credentials Secret and the trust anchor" {
  rendered=$(helm template mariadb-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  # eval-all again: with `eval`, a second rendered document would emit a second
  # JSON object and the comparison below would run on a concatenation.
  actual=$(printf '%s\n' "$rendered" \
    | yq eval-all --output-format=json '[.. | select(has("secrets")) | .secrets]' - \
    | jq --sort-keys --compact-output "if length == 1 then .[0] else . end | $NORMALIZE") || return 1

  expected=$(printf '%s' "$EXPECTED_SECRETS" \
    | jq --sort-keys --compact-output "$NORMALIZE") || return 1

  # Fail closed: an empty or null result means spec.secrets stopped being
  # readable at this path, so the guard is no longer observing what it claims.
  if [ -z "$actual" ] || [ "$actual" = "null" ]; then
    echo "spec.secrets is missing or unreadable in the rendered ResourceDefinition -- guard is blind." >&2
    return 1
  fi

  if [ "$actual" != "$expected" ]; then
    echo "Unexpected tenant Secret surface in the rendered mariadb-rd spec.secrets:" >&2
    echo "  actual:   $actual" >&2
    echo "  expected: $expected" >&2
    echo "Both lists are pinned: include decides what a tenant receives, and" >&2
    echo "exclude is evaluated first and wins outright, so an entry added there" >&2
    echo "revokes the connection string or the trust anchor in silence." >&2
    return 1
  fi
}

@test "mariadb-rd exposes exactly the instance Services" {
  rendered=$(helm template mariadb-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  actual=$(printf '%s\n' "$rendered" \
    | yq eval-all --output-format=json '[.. | select(has("services")) | .services]' - \
    | jq --sort-keys --compact-output "if length == 1 then .[0] else . end | $NORMALIZE") || return 1

  expected=$(printf '%s' "$EXPECTED_SERVICES" \
    | jq --sort-keys --compact-output "$NORMALIZE") || return 1

  if [ -z "$actual" ] || [ "$actual" = "null" ]; then
    echo "spec.services is missing or unreadable in the rendered ResourceDefinition -- guard is blind." >&2
    return 1
  fi

  if [ "$actual" != "$expected" ]; then
    echo "Unexpected tenant Service surface in the rendered mariadb-rd spec.services:" >&2
    echo "  actual:   $actual" >&2
    echo "  expected: $expected" >&2
    return 1
  fi
}
