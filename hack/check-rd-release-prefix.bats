#!/usr/bin/env bats
# "contains <prefix>{{ .name }}" rather than a prefix match: operator-created
# objects carry their own leading words, such as redis's "rfs-redis-{{ .name }}"
# and mongodb's excluded "internal-mongodb-{{ .name }}-users".
#
# An empty spec.release.prefix is skipped: hack/update-crd.sh generates the
# definitions for packages/extra charts with PREFIX="".

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "every resourceNames selector carries its release prefix" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }

  violations=""
  total=0
  for f in "$REPO_ROOT"/packages/system/*-rd/cozyrds/*.yaml; do
    [ -f "$f" ] || continue
    prefix="$(yq -r '.spec.release.prefix // ""' "$f")"
    [ -n "$prefix" ] || continue

    # One "<section>.<include|exclude> TAB <name> TAB <line comment>" row per
    # selector entry. yq sees the comment only on a block-style entry: on a
    # flow sequence ("[a, b] # ...") it belongs to the list, not to an entry.
    rows="$(yq -r '
      .spec | (.secrets, .services, .ingresses) | select(. != null)
      | (.include, .exclude) | select(. != null) | .[]
      | .resourceNames | select(. != null) | .[]
      | [(path | .[1] + "." + .[2]), ., (line_comment // "")] | @tsv
    ' "$f")"
    [ -n "$rows" ] || continue
    total=$(( total + $(printf '%s\n' "$rows" | grep -c .) ))

    bad="$(printf '%s\n' "$rows" | awk -F '\t' -v needle="${prefix}{{ .name }}" -v file="${f#"$REPO_ROOT"/}" '
      $3 ~ /cozy-lint:ignore rd-release-prefix/ {
        if ($3 !~ /cozy-lint:ignore rd-release-prefix -- [^ ]/) print file ": " $1 ": " $2 " (exempted without a reason)"
        next
      }
      index($2, needle) == 0 { print file ": " $1 ": " $2 }
    ')"
    [ -z "$bad" ] || violations="$violations$bad
"
  done

  # A glob that matched nothing, or a yq that stopped returning names, would
  # otherwise pass silently.
  [ "$total" -ge 1 ] || { echo "inspected zero selectors; this test is broken" >&2; exit 1; }

  if [ -n "$violations" ]; then
    echo "resourceNames selectors that can never match the object their chart creates:" >&2
    printf '%s' "$violations" >&2
    echo "Fix: spell the release prefix out, e.g. \"harbor-{{ .name }}-credentials\"." >&2
    echo "A name deliberately not derived from the release takes a trailing comment on its entry:" >&2
    echo "  # cozy-lint:ignore rd-release-prefix -- <reason>" >&2
    exit 1
  fi
}
