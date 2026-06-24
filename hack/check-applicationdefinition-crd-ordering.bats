#!/usr/bin/env bats
# Chart-wide invariant for packages/core/platform/sources:
#
# Every *-application PackageSource that ships a `system/*-rd` component renders
# an ApplicationDefinition (cozystack.io/v1alpha1). That CRD is installed by the
# application-definition-crd component of cozystack.cozystack-engine. Without an
# explicit `dependsOn: cozystack.cozystack-engine`, a fresh install where Flux
# happens to reconcile the *-application source before cozystack-engine fails to
# render with `no matches for kind "ApplicationDefinition" in version
# "cozystack.io/v1alpha1"`, which cascades into a broad install timeout.
#
# This invariant is generic: any future *-application source that adds a
# system/*-rd component but forgets the cozystack-engine edge fails here, even
# though the individual source files are not enumerated below.
#
# Requires: yq (mikefarah v4+), jq. Both are available on the project's CI
# runners and on the maintainer workstation.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SOURCES_DIR="$REPO_ROOT/packages/core/platform/sources"

@test "every PackageSource with a system/*-rd component dependsOn cozystack.cozystack-engine" {
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }
  command -v jq >/dev/null || { echo "jq is required" >&2; exit 1; }

  # yq streams one JSON object per source document; jq -s slurps the stream so
  # every PackageSource variant can be evaluated as a single collection.
  json="$(yq --output-format=json eval-all '.' "$SOURCES_DIR"/*.yaml)"

  # A "-rd variant" is any variant whose components include a system/*-rd path.
  rd_total="$(printf '%s' "$json" | jq -s '
    [ .[]
      | select(.kind == "PackageSource")
      | (.spec.variants // [])[]
      | select((.components // []) | any(.path // "" | test("^system/[a-z0-9-]+-rd$")))
    ] | length
  ')"

  # Guard against a vacuous pass (empty glob / yq breakage): the platform ships
  # many -rd sources, so discovering zero means this test itself is broken.
  [ "$rd_total" -ge 1 ] || { echo "Discovered zero system/*-rd variants under $SOURCES_DIR - test is misconfigured" >&2; exit 1; }

  offenders="$(printf '%s' "$json" | jq -rs '
    [ .[]
      | select(.kind == "PackageSource")
      | .metadata.name as $name
      | (.spec.variants // [])[]
      | select((.components // []) | any(.path // "" | test("^system/[a-z0-9-]+-rd$")))
      | select(((.dependsOn // []) | any(. == "cozystack.cozystack-engine")) | not)
      | "\($name) [variant: \(.name)]"
    ] | .[]
  ')"

  if [ -n "$offenders" ]; then
    echo "PackageSources with a system/*-rd component but missing the cozystack.cozystack-engine dependsOn edge:" >&2
    printf '%s\n' "$offenders" >&2
    echo "Fix: add '- cozystack.cozystack-engine' to each listed variant's dependsOn (mirror sources/gateway-application.yaml)." >&2
    exit 1
  fi

  echo "Invariant holds: all $rd_total system/*-rd PackageSource variant(s) dependsOn cozystack.cozystack-engine"
}
