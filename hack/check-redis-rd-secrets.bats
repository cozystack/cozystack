#!/usr/bin/env bats
# Unit test: the redis ResourceDefinition hands tenants the key-free trust
# anchor and nothing else.
#
# The chart issues redis-<name>.ca-tls (CA certificate AND CA private key, the
# latter under tls.key) and redis-<name>.tls (server certificate AND server
# private key). Neither may reach a tenant: read access to the CA key would
# let the holder issue certificates for anything rather than merely verify the
# server. What a client actually needs is ca.crt alone, delivered by the
# CA-extraction controller as the key-free <release>.tenant-ca projection and
# selected by LABEL rather than by name, because a name grant conveys whatever
# happens to occupy that name.
#
# Redis has a third Secret the other engines do not: redis-<name>.ca-cert, the
# CA-only copy the patched operator publishes for itself. It is key-free today,
# which is exactly why it is tempting to grant by name — and why it is not.
# The operator holds create and update on Secrets cluster-wide, so what lands
# under that name is the operator's choice rather than the platform's, and RBAC
# has no key-level filter to fall back on.
#
# Asserted against the RENDERED chart, not against cozyrds/redis.yaml on
# disk. templates/cozyrd.yaml globs cozyrds/* and emits each match through
# .Files.Get, so what ships is the render: reading one source file would miss
# a second file in that directory, and a dotfile the glob picks up. Helm
# ignores dotfiles only under templates/, so one here does ship.
#
# This lives in bats rather than helm-unittest because packages/system/
# redis-rd has no `test:` target, and hack/helm-unit-tests.sh skips any
# package without one. If that package ever grows one, this belongs there.
#
# spec.secrets is compared WHOLE rather than probed for known-bad names.
# Three ways this breaks are invisible to a name-by-name check:
#
#   - a name added under a different spelling, or via a helper;
#   - an include with NO resourceNames and NO matchLabels, which restricts
#     nothing;
#   - a SECOND matchLabels include selecting a label the key-bearing Secrets
#     also carry. cert-manager stamps controller.cert-manager.io/fao on the
#     Secrets it creates, both of these among them, so one such entry grants
#     the CA private key while every name in the file stays correct.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART="$REPO_ROOT/packages/system/redis-rd"

# The complete tenant-visible Secret surface. The generated password by name;
# the trust anchor by label. Nothing else. The two key-bearing Secrets are
# named in exclude, which is evaluated before include and so backstops the
# label-only entry against a mis-stamp.
EXPECTED_SECRETS='{"exclude":[{"resourceNames":["redis-{{ .name }}.ca-tls","redis-{{ .name }}.tls","redis-{{ .name }}.ca-cert"]}],"include":[{"resourceNames":["redis-{{ .name }}-auth"]},{"matchLabels":{"internal.cozystack.io/tenant-ca":"true"}}]}'

# Order within include is meaningless -- the matcher ORs across entries -- so
# both arrays are sorted before comparing, and a missing exclude is normalized
# to the empty list it means. Neither can hide a grant: sort preserves every
# entry (it is not unique), and exclude only ever narrows.
#
# Kept as a filter string rather than a shell function because hack/cozytest.sh
# inserts `return 0` before any line that is exactly `}`. A helper whose own
# last command decides the outcome returns 0 regardless -- and whether a
# wrapper propagates a failure at all then depends on the shell running it.
# Repeating the helm invocation inline in each test costs two lines and owes
# nothing to that.
NORMALIZE='{exclude: ((.exclude // []) | sort), include: (.include | sort)}'

# Every document count below uses `yq eval-all`, never `yq eval`. `eval` runs
# the expression once PER DOCUMENT, so `[.] | length` reports 1 per document
# rather than the number of documents -- it prints "1" for a single document,
# "1\n1" for two, and "1" for empty input, which would make a count check here
# pass on exactly the cases it exists to catch. `eval-all` folds the stream
# into one expression. Empty input still yields 1, since yq synthesizes a null
# document, so emptiness is checked separately before counting.

@test "redis-rd renders exactly one ApplicationDefinition" {
  # stderr is deliberately not discarded: when the render breaks, helm's own
  # message is the only thing that says why.
  rendered=$(helm template redis-rd "$CHART") || return 1

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

@test "redis-rd selects the key-free tenant-CA projection by label" {
  rendered=$(helm template redis-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  count=$(printf '%s\n' "$rendered" | yq eval-all \
    '[.. | select(has("secrets")) | .secrets.include[] | select(.matchLabels."internal.cozystack.io/tenant-ca" == "true")] | length' \
    -) || return 1

  if [ "$count" -lt 1 ]; then
    echo 'redis-rd does not select internal.cozystack.io/tenant-ca: "true"' >&2
    echo "Without it the tenant cannot read ca.crt and TLS verification is impossible." >&2
    return 1
  fi
}

# Whole-structure comparison. Anything added to the tenant's Secret surface --
# by name, by label, or by an unrestricted entry -- changes this string.
@test "redis-rd exposes no Secret beyond the password and the trust anchor" {
  rendered=$(helm template redis-rd "$CHART") || return 1

  if [ -z "$rendered" ]; then
    echo "helm template produced no output for $CHART -- guard is blind." >&2
    return 1
  fi

  # eval-all again: with `eval`, a second rendered document would emit a
  # second JSON object and the comparison below would run on a concatenation.
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
    echo "Unexpected tenant Secret surface in the rendered redis-rd spec.secrets:" >&2
    echo "  actual:   $actual" >&2
    echo "  expected: $expected" >&2
    echo "redis-<name>.ca-tls holds the CA PRIVATE KEY and redis-<name>.tls the" >&2
    echo "server private key. Neither may be granted to a tenant, by name or by" >&2
    echo "a label they also carry -- cert-manager stamps its own labels on both." >&2
    echo "redis-<name>.ca-cert is key-free but operator-written, so it is not" >&2
    echo "granted either; the trust anchor travels as the tenant-ca projection." >&2
    return 1
  fi
}
