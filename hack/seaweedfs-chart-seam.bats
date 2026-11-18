#!/usr/bin/env bats
# Unit test: the volume size limit the SeaweedFS app chart writes into its
# <name>-system HelmRelease reaches the master the data-plane chart renders,
# with the growth counts that make up the rest of the bucket budget intact.
#
# Each chart has its own suite and neither feeds the other: the app chart's
# asserts on the spec.values it writes, the data-plane chart's on the command it
# renders from its own defaults. A key the app chart writes and the data-plane
# chart no longer reads, or one a chart bump moves, leaves both green. This
# renders the app chart, lifts spec.values out of the HelmRelease and renders
# the data-plane chart with exactly that, the way helm-controller does.
#
# What it leaves out: the tenant's cozystack-values Secret, which the release
# also takes through valuesFrom; the tenant chart writes only _cluster and
# _namespace into it (packages/apps/tenant/templates/namespace.yaml). The live
# merge, Secret included, is asserted by hack/e2e-chainsaw/seaweedfs/.
#
# Run with: bats hack/seaweedfs-chart-seam.bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
APP_CHART="$REPO_ROOT/packages/extra/seaweedfs"
SYSTEM_CHART="$REPO_ROOT/packages/system/seaweedfs"

# render_master <workdir> [helm args...] -- the master StatefulSet the
# data-plane chart renders from the app chart's output; leaves the master
# script in <workdir>/command and its env in <workdir>/env.
render_master() {
  dir=$1
  shift
  helm template seaweedfs "$APP_CHART" --namespace tenant-root \
    --set _namespace.host=example.org --set _namespace.ingress=tenant-root \
    --set _cluster.solver=http01 "$@" \
    --show-only templates/seaweedfs.yaml > "$dir/app.yaml"
  yq 'select(.metadata.name == "seaweedfs-system") | .spec.values' \
    "$dir/app.yaml" > "$dir/values.yaml"
  # Premise: an empty lift would render the data-plane defaults and pass the
  # COPY_* checks on its own.
  grep -q '^seaweedfs:' "$dir/values.yaml"
  helm template seaweedfs-system "$SYSTEM_CHART" --namespace tenant-root \
    --values "$dir/values.yaml" \
    --show-only charts/seaweedfs/templates/master/master-statefulset.yaml > "$dir/master.yaml"
  yq '.spec.template.spec.containers[0].command[2]' "$dir/master.yaml" > "$dir/command"
  yq -o=json -I=0 '.spec.template.spec.containers[0].env[] | select(.name | test("^WEED_MASTER_VOLUME_GROWTH_")) | [.name, .value]' \
    "$dir/master.yaml" > "$dir/env"
}

# The four growth counts, as the master must see them: COPY_2 from the
# data-plane chart's own values, the other three from the subchart beneath it.
# The app chart sends a master block of its own, and a merge that replaced
# rather than merged it would drop all four.
EXPECTED_GROWTH='["WEED_MASTER_VOLUME_GROWTH_COPY_1","7"]
["WEED_MASTER_VOLUME_GROWTH_COPY_2","3"]
["WEED_MASTER_VOLUME_GROWTH_COPY_3","3"]
["WEED_MASTER_VOLUME_GROWTH_COPY_OTHER","1"]'

@test "the app chart's default volume size limit starts the master" {
  # Read rather than pinned: the default itself is pinned in the app chart's
  # own suite, this checks only that it arrives. It has to differ from the
  # data-plane fallback, or a dropped hand-off would render the same flag.
  app=$(yq '.master.volumeSizeLimitMB' "$APP_CHART/values.yaml")
  fallback=$(yq '.seaweedfs.master.volumeSizeLimitMB' "$SYSTEM_CHART/values.yaml")
  case "$app" in ''|*[!0-9]*) echo "app chart default is not a number: '$app'" >&2; exit 1 ;; esac
  case "$fallback" in ''|*[!0-9]*) echo "data-plane fallback is not a number: '$fallback'" >&2; exit 1 ;; esac
  [ "$app" != "$fallback" ]
  dir=$(mktemp -d)
  render_master "$dir"
  grep -qE -- "-volumeSizeLimitMB=$app( |\$)" "$dir/command"
  [ "$(cat "$dir/env")" = "$EXPECTED_GROWTH" ]
  rm -rf "$dir"
}

@test "an operator's volume size limit starts the master" {
  dir=$(mktemp -d)
  render_master "$dir" --set master.volumeSizeLimitMB=500
  grep -qE -- '-volumeSizeLimitMB=500( |$)' "$dir/command"
  [ "$(cat "$dir/env")" = "$EXPECTED_GROWTH" ]
  rm -rf "$dir"
}
