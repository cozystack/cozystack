#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Ties the kubernetes app's default Talos image to the kubernetes-worker-image
# catalog's default golden. The two live in independently edited files:
#
#   packages/apps/kubernetes/values.yaml            talos.{schematicID,version}
#   packages/system/kubernetes-worker-image/values.yaml   images[], storageClass
#
# A node group that opts in with `image.builtin: {}` resolves the golden it
# clones from the APP's talos defaults, then requires the catalog to hold a
# matching (schematicID, version) — packages/apps/kubernetes/templates/
# cluster.yaml fails the whole render when it does not. So a one-sided Talos bump
# silently breaks every fresh install using image.builtin, and nothing catches it
# until a ~95-minute e2e run does.
#
# The StorageClass is the same trap from the other direction: CDI cannot
# CSI-clone across StorageClasses (it would silently fall back to a host-assisted
# copy over the pod network), so the render also rejects a group whose
# storageClass differs from its golden's. Bumping one file's default alone brings
# that guard down on the default path.
#
# Both checks are cheap and exact, which is the point — they turn a slow, remote
# e2e failure into a local one-second unit test.
#
# Compatible with both `bats` directly and the in-repo cozytest.sh runner, which
# runs each @test in a fresh subshell with `set -u` and does not honor bats
# setup()/teardown().

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
APP_VALUES="$REPO_ROOT/packages/apps/kubernetes/values.yaml"
CATALOG_VALUES="$REPO_ROOT/packages/system/kubernetes-worker-image/values.yaml"

@test "worker image catalog holds a golden for the app's default Talos (schematicID, version)" {
  sid=$(yq '.talos.schematicID' "$APP_VALUES")
  ver=$(yq '.talos.version' "$APP_VALUES")
  [ -n "$sid" ] && [ "$sid" != "null" ]
  [ -n "$ver" ] && [ "$ver" != "null" ]

  # Any entry may match — the catalog is a list of flavors, not an ordered pair.
  if ! yq '.images[] | .schematicID + " " + .version' "$CATALOG_VALUES" \
       | grep -qxF "$sid $ver"; then
    echo "packages/apps/kubernetes/values.yaml defaults to Talos:" >&2
    echo "  schematicID: $sid" >&2
    echo "  version:     $ver" >&2
    echo "but the kubernetes-worker-image catalog has no matching images[] entry:" >&2
    yq '.images[] | "  - " + .schematicID + " " + .version' "$CATALOG_VALUES" >&2
    echo "Every node group using 'image.builtin: {}' would fail to render." >&2
    echo "Add the pair to the catalog, or bump both files together." >&2
    return 1
  fi
}

@test "worker image catalog golden shares the app's default StorageClass" {
  sid=$(yq '.talos.schematicID' "$APP_VALUES")
  ver=$(yq '.talos.version' "$APP_VALUES")
  app_sc=$(yq '.storageClass' "$APP_VALUES")

  # The golden's effective class is its own override when set, else the catalog
  # default — mirroring how the app resolves a node group's class.
  entry_sc=$(SID="$sid" VER="$ver" yq \
    '.images[] | select(.schematicID == strenv(SID) and .version == strenv(VER)) | .storageClass' \
    "$CATALOG_VALUES")
  if [ -z "$entry_sc" ] || [ "$entry_sc" = "null" ]; then
    entry_sc=$(yq '.storageClass' "$CATALOG_VALUES")
  fi

  if [ "$app_sc" != "$entry_sc" ]; then
    echo "StorageClass mismatch on the default image.builtin path:" >&2
    echo "  packages/apps/kubernetes/values.yaml storageClass:        $app_sc" >&2
    echo "  kubernetes-worker-image golden effective storageClass:    $entry_sc" >&2
    echo "CDI cannot CSI-clone across StorageClasses, so the tenant render" >&2
    echo "rejects this outright. Keep the two defaults in step." >&2
    return 1
  fi
}
