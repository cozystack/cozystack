#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# `osImage` of a KubernetesNodes pool is one value: `builtin` and `factory` exclude
# each other, and the chart refuses a pool that sets both. A default merged into a
# pool that already chose the other source would build exactly that object, so the
# schema marks the field `x-kubernetes-map-type: atomic` and the merge that fills
# creation defaults replaces the object whole instead of joining the two.
#
# The marker is written as `## @x-kubernetes-map-type atomic` in values.yaml and
# reaches two generated files. Losing it from either is silent: the chart still
# renders, and the failure shows up as a pool that is refused at create time. The
# second test also pins the other direction, because a marker on a field whose
# children are independent would stop a user's partial value from being completed.
#
# Compatible with both `bats` directly and the in-repo cozytest.sh runner.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
SCHEMA="$REPO_ROOT/packages/apps/kubernetes-nodes/values.schema.json"
RD="$REPO_ROOT/packages/system/kubernetes-nodes-rd/cozyrds/kubernetes-nodes.yaml"

# Dotted paths of every x-kubernetes-map-type key in a JSON document on stdin.
map_type_paths() {
  jq -r '[paths(type == "string") as $p | select(($p | last) == "x-kubernetes-map-type") | ($p | map(tostring) | join("."))] | join(" ")'
}

@test "osImage is atomic in the pool values schema, and it is the only object that is" {
  got=$(map_type_paths < "$SCHEMA")
  [ "$got" = "properties.osImage.x-kubernetes-map-type" ] || { echo "x-kubernetes-map-type found at [$got] in values.schema.json, want only properties.osImage" >&2; exit 1; }
  [ "$(jq -r '.properties.osImage."x-kubernetes-map-type"' "$SCHEMA")" = "atomic" ]
}

@test "osImage is atomic in the KubernetesNodes ApplicationDefinition, and it is the only object that is" {
  schema=$(yq -o=json '.spec.application.openAPISchema' "$RD" | jq -r 'fromjson')
  got=$(printf '%s' "$schema" | map_type_paths)
  [ "$got" = "properties.osImage.x-kubernetes-map-type" ] || { echo "x-kubernetes-map-type found at [$got] in the ApplicationDefinition schema, want only properties.osImage" >&2; exit 1; }
  [ "$(printf '%s' "$schema" | jq -r '.properties.osImage."x-kubernetes-map-type"')" = "atomic" ]
}
