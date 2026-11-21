#!/usr/bin/env bats
# The cozystack.opencost guard in packages/core/platform/templates/bundles/system.yaml
# fails the render when a Package OpenCost transitively depends on is disabled
# or, on a slim variant, not enabled. Both lists are written out by hand, so
# they go stale the day a PackageSource in that closure gains or drops a
# dependsOn edge, or a package in it changes between always-on and opt-in.
#
# This derives the closure from packages/core/platform/sources for both OIDC
# modes and compares it with $opencostNeeds. The slim opt-in part cannot be read
# from sources/, so tests/bundles_opencost_default_test.yaml renders a slim
# variant per OIDC mode and asserts which closure packages it emits; this checks
# that those assertions cover the whole closure and that the absent ones are
# exactly $opencostSlimOptIn.
#
# cozystack.networking is not walked: the guard leaves it out on purpose, since
# listing it in disabledPackages is how a CNI managed by hand is kept.
#
# Requires: yq (mikefarah v4+), jq.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SOURCES_DIR="$REPO_ROOT/packages/core/platform/sources"
SYSTEM_BUNDLE="$REPO_ROOT/packages/core/platform/templates/bundles/system.yaml"
OPENCOST_TESTS="$REPO_ROOT/packages/core/platform/tests/bundles_opencost_default_test.yaml"

require_tools() {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }
}

# Closure of cozystack.opencost, one name per line. With $1 = oidc, a source
# takes its oidc variant only where the system bundle includes it that way.
opencost_closure() {
  oidc_sources="$(grep -o '(list "cozystack\.[a-z0-9-]*" "oidc"' "$SYSTEM_BUNDLE" | grep -o 'cozystack\.[a-z0-9-]*' | jq -R . | jq -s .)"
  yq --output-format=json eval-all '.' "$SOURCES_DIR"/*.yaml | jq -rs --arg mode "$1" --argjson oidc "$oidc_sources" '
    (map(select(.kind == "PackageSource")) | map({key: .metadata.name, value: (.spec.variants // [])}) | from_entries) as $src
    | def deps($n):
        ($src[$n] // []) as $vs
        | (if $mode == "oidc" and ($oidc | index($n)) then "oidc" else "default" end) as $v
        | (($vs | map(select(.name == $v)) | first) // {}) | .dependsOn // [];
      def walk($todo; $seen):
        if ($todo | length) == 0 then $seen
        else ([deps($todo[0])[] | select(. != "cozystack.networking")] - $seen - $todo) as $new
          | walk($todo[1:] + $new; $seen + $new)
        end;
      walk(["cozystack.opencost"]; []) | unique | .[]
  '
}

# Names listed on the template line matching $1.
guard_list() {
  grep -F -- "$1" "$SYSTEM_BUNDLE" | grep -o '"cozystack\.[a-z0-9-]*"' | tr -d '"'
}

# Package names the unittest named $1 asserts as emitted ($2 = present) or
# absent ($2 = absent).
slim_render() {
  yq -r ".tests[] | select(.it == \"$1\") | .asserts[] | select(.containsDocument.kind == \"Package\")
    | select((.not // false) == (\"$2\" == \"absent\")) | .containsDocument.name" "$OPENCOST_TESTS"
}

same_set() {
  what=$1 expected=$2 actual=$3
  if [ "$expected" != "$actual" ]; then
    echo "$what:" >&2
    diff <(printf '%s\n' "$expected") <(printf '%s\n' "$actual") | sed -n 's/^< /  missing: /p; s/^> /  unexpected: /p' >&2
    exit 1
  fi
}

needs_default() { guard_list '$opencostNeeds := list' | sort; }
needs_oidc() { { guard_list '$opencostNeeds := list'; guard_list '$opencostNeeds = concat $opencostNeeds'; } | sort; }
optin_default() { { guard_list '$opencostSlimOptIn := list'; guard_list '$opencostSlimOptIn = append $opencostSlimOptIn'; } | sort; }
optin_oidc() { guard_list '$opencostSlimOptIn := list' | sort; }

check_closure() {
  derived="$(opencost_closure "$1" | sort)"
  [ -n "$derived" ] || { echo "derived an empty closure for cozystack.opencost from $SOURCES_DIR; the test is broken" >&2; exit 1; }
  same_set "\$opencostNeeds in $SYSTEM_BUNDLE against the dependsOn closure in sources/ ($1)" "$derived" "$2"
}

check_slim() {
  test_name=$1 closure=$2 optin=$3
  present="$(slim_render "$test_name" present | sort)"
  absent="$(slim_render "$test_name" absent | sort)"
  [ -n "$absent" ] || { echo "no unittest named \"$test_name\" with absent Packages in $OPENCOST_TESTS" >&2; exit 1; }
  same_set "Packages asserted in \"$test_name\" against the closure" "$closure" "$(printf '%s\n%s\n' "$present" "$absent" | sort)"
  same_set "\$opencostSlimOptIn in $SYSTEM_BUNDLE against the Packages \"$test_name\" asserts absent" "$absent" "$optin"
}

@test "the opencost guard lists the dependsOn closure of cozystack.opencost without OIDC" {
  require_tools
  check_closure default "$(needs_default)"
}

@test "the opencost guard lists the dependsOn closure of cozystack.opencost with OIDC" {
  require_tools
  check_closure oidc "$(needs_oidc)"
}

@test "the opencost slim opt-in list is the part of the closure a slim render leaves out, without OIDC" {
  require_tools
  check_slim "on a slim variant emits only the always-on part of the OpenCost closure" "$(opencost_closure default | sort)" "$(optin_default)"
}

@test "the opencost slim opt-in list is the part of the closure a slim render leaves out, with OIDC" {
  require_tools
  check_slim "on a slim variant with OIDC emits only the always-on part of the OpenCost closure" "$(opencost_closure oidc | sort)" "$(optin_oidc)"
}
