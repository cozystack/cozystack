#!/usr/bin/env bats
# A one-sided legacy-default/catalog change must fail before a builtin pool
# waits for a golden that cannot exist. API defaults are not this source.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
APP_VALUES="$REPO_ROOT/packages/apps/kubernetes-nodes/values.yaml"
APP_DEFAULTS="$REPO_ROOT/packages/apps/kubernetes-nodes/files/worker-defaults.json"
CATALOG_VALUES="$REPO_ROOT/packages/system/kubernetes-worker-image/values.yaml"

@test "worker image catalog holds a golden for the pool chart's default Talos (schematicID, version)" {
  sid=$(yq --unwrapScalar '.talos.schematicID' "$APP_DEFAULTS")
  ver=$(yq --unwrapScalar '.talos.version' "$APP_DEFAULTS")
  [ -n "$sid" ] && [ "$sid" != "null" ]
  [ -n "$ver" ] && [ "$ver" != "null" ]

  # Any entry may match — the catalog is a list of flavors, not an ordered pair.
  if ! yq '.images[] | .schematicID + " " + .version' "$CATALOG_VALUES" \
       | grep -qxF "$sid $ver"; then
    echo "legacy worker defaults select Talos:" >&2
    echo "  schematicID: $sid" >&2
    echo "  version:     $ver" >&2
    echo "but the kubernetes-worker-image catalog has no matching images[] entry:" >&2
    yq '.images[] | "  - " + .schematicID + " " + .version' "$CATALOG_VALUES" >&2
    echo "Every worker pool using 'osImage.builtin: {}' would fail to render." >&2
    echo "Add the pair to the catalog, or bump both files together." >&2
    return 1
  fi
}

@test "worker image catalog golden shares the pool chart's default StorageClass" {
  sid=$(yq --unwrapScalar '.talos.schematicID' "$APP_DEFAULTS")
  ver=$(yq --unwrapScalar '.talos.version' "$APP_DEFAULTS")
  app_sc=$(yq '.storageClass' "$APP_VALUES")

  # The golden's effective class is its own override when set, else the catalog
  # default — mirroring how the pool chart resolves its class.
  entry_sc=$(SID="$sid" VER="$ver" yq \
    '.images[] | select(.schematicID == strenv(SID) and .version == strenv(VER)) | .storageClass' \
    "$CATALOG_VALUES")
  if [ -z "$entry_sc" ] || [ "$entry_sc" = "null" ]; then
    entry_sc=$(yq '.storageClass' "$CATALOG_VALUES")
  fi

  if [ "$app_sc" != "$entry_sc" ]; then
    echo "StorageClass mismatch on the default osImage.builtin path:" >&2
    echo "  packages/apps/kubernetes-nodes/values.yaml storageClass:  $app_sc" >&2
    echo "  kubernetes-worker-image golden effective storageClass:    $entry_sc" >&2
    echo "CDI cannot CSI-clone across StorageClasses, so the tenant render" >&2
    echo "rejects this outright. Keep the two defaults in step." >&2
    return 1
  fi
}

NODES_TEMPLATE="$REPO_ROOT/packages/apps/kubernetes-nodes/templates/nodegroup.yaml"
NODES_SCHEMA="$REPO_ROOT/packages/apps/kubernetes-nodes/values.schema.json"

@test "the support matrix is consulted for the image-resolved Talos version, not only talos.version" {
  calls=$(grep -c 'include "kubernetes-nodes.assertTalosSupportsKubernetes"' "$NODES_TEMPLATE" || true)
  if [ "$calls" -lt 2 ]; then
    echo "nodegroup.yaml calls the Talos support-matrix guard $calls time(s); it needs one for talos.version and one for the version osImage.builtin/osImage.factory resolves to" >&2
    echo "without the second call a pool keeps a supported talos.version and still boots an unsupported Talos minor through its image override, with no render failure" >&2
    return 1
  fi
  # And the second call must be fed the RESOLVED version. Passing talos.version
  # twice would satisfy the count above while checking nothing new.
  if ! grep -q '"talosVersion" \$reqVersion' "$NODES_TEMPLATE"; then
    echo "the second support-matrix call does not pass \$reqVersion, so it re-checks the value the first call already checked" >&2
    return 1
  fi
}

@test "the support matrix covers every Kubernetes version the schema offers, for the default Talos" {
  ver=$(yq --unwrapScalar '.talos.version' "$APP_DEFAULTS")
  minor=$(printf '%s\n' "$ver" | grep -oE '^v[0-9]+\.[0-9]+')
  [ -n "$minor" ]

  row=$(MINOR="$minor" yq --output-format=json '.talosKubernetesSupport[strenv(MINOR)]' "$APP_DEFAULTS")
  if [ -z "$row" ] || [ "$row" = null ]; then
    echo "shared defaults have no support row for $minor" >&2
    return 1
  fi

  # Every version the schema lets an operator pick must be in that row, or the
  # guard rejects a combination the API advertises as valid. Capture the enum
  # first and refuse an empty read: iterating straight over the command
  # substitution cannot tell "no versions" from "that query found nothing", so a
  # renamed or relocated property would run the loop zero times and report
  # success -- the drift this test exists to catch would be the change that
  # disarms it.
  enum=$(yq -r '.properties.version.enum[]' "$NODES_SCHEMA" || true)
  if [ -z "$enum" ]; then
    echo "could not read .properties.version.enum from $NODES_SCHEMA" >&2
    echo "the enum was renamed, moved, or the file relocated; this guard cannot check the matrix without it" >&2
    return 1
  fi
  for k in $enum; do
    case "$row" in
      *"\"$k\""*) ;;
      *)
        echo "values.schema.json offers version $k, but the Talos ${minor} matrix row does not list it:" >&2
        echo "  $row" >&2
        echo "either the row is stale or the enum gained a version Talos ${minor} does not support" >&2
        return 1 ;;
    esac
  done
}

PARENT_VALUES="$REPO_ROOT/packages/apps/kubernetes/values.yaml"

@test "the parent kubernetes chart and kubernetes-nodes agree on their Talos defaults" {
  for key in version schematicID imageFactoryURL installerRepository registryMirrors; do
    parent=$(yq --output-format=json ".talos.${key}" "$PARENT_VALUES")
    pool=$(yq --output-format=json ".talos.${key}" "$APP_DEFAULTS")
    if [ "$parent" != "$pool" ]; then
      echo "talos.${key} differs between the two charts:" >&2
      echo "  packages/apps/kubernetes/values.yaml:       $parent" >&2
      echo "  packages/apps/kubernetes-nodes/files/worker-defaults.json: $pool" >&2
      echo "legacy defaults are declared 'keep in sync' and a worker pool follows the second while the parent's support-matrix guard reads the first" >&2
      return 1
    fi
  done
}
