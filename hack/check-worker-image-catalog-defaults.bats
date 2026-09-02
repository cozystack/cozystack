#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Ties the kubernetes-nodes chart's default Talos image to the kubernetes-worker-image
# catalog's default golden. The two live in independently edited files:
#
#   packages/apps/kubernetes-nodes/values.yaml      talos.{schematicID,version}
#   packages/system/kubernetes-worker-image/values.yaml   images[], storageClass
#
# A worker pool that opts in with `image.builtin: {}` resolves the golden it
# clones from the POOL chart's talos defaults, then requires the catalog to hold a
# matching (schematicID, version) — packages/apps/kubernetes-nodes/templates/
# nodegroup.yaml fails the whole render when it does not. So a one-sided Talos bump
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
APP_VALUES="$REPO_ROOT/packages/apps/kubernetes-nodes/values.yaml"
CATALOG_VALUES="$REPO_ROOT/packages/system/kubernetes-worker-image/values.yaml"

@test "worker image catalog holds a golden for the pool chart's default Talos (schematicID, version)" {
  sid=$(yq '.talos.schematicID' "$APP_VALUES")
  ver=$(yq '.talos.version' "$APP_VALUES")
  [ -n "$sid" ] && [ "$sid" != "null" ]
  [ -n "$ver" ] && [ "$ver" != "null" ]

  # Any entry may match — the catalog is a list of flavors, not an ordered pair.
  if ! yq '.images[] | .schematicID + " " + .version' "$CATALOG_VALUES" \
       | grep -qxF "$sid $ver"; then
    echo "packages/apps/kubernetes-nodes/values.yaml defaults to Talos:" >&2
    echo "  schematicID: $sid" >&2
    echo "  version:     $ver" >&2
    echo "but the kubernetes-worker-image catalog has no matching images[] entry:" >&2
    yq '.images[] | "  - " + .schematicID + " " + .version' "$CATALOG_VALUES" >&2
    echo "Every worker pool using 'image.builtin: {}' would fail to render." >&2
    echo "Add the pair to the catalog, or bump both files together." >&2
    return 1
  fi
}

@test "worker image catalog golden shares the pool chart's default StorageClass" {
  sid=$(yq '.talos.schematicID' "$APP_VALUES")
  ver=$(yq '.talos.version' "$APP_VALUES")
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
    echo "StorageClass mismatch on the default image.builtin path:" >&2
    echo "  packages/apps/kubernetes-nodes/values.yaml storageClass:  $app_sc" >&2
    echo "  kubernetes-worker-image golden effective storageClass:    $entry_sc" >&2
    echo "CDI cannot CSI-clone across StorageClasses, so the tenant render" >&2
    echo "rejects this outright. Keep the two defaults in step." >&2
    return 1
  fi
}

# -----------------------------------------------------------------------------
# Second family of default-drift guards on the same chart: the Talos <-> Kubernetes
# support matrix. Reviewed as [MAJOR] on cozystack/cozystack#3294 — the render
# guard keyed only on `talos.version`, so a pool could reach a different Talos
# release through `image.builtin.version` / `image.factory.version` and boot a
# kubelet outside its support window with no render failure. The table now lives
# once in templates/_helpers.tpl and both paths consult it.
#
# The symmetry is structural, not observable from a render: the matrix ships one
# row (`v1.13`) whose Kubernetes list is a superset of `values.schema.json`'s
# `version` enum, so no schema-valid pairing can make either call site fail today.
# A helm-unittest case would have to put an out-of-enum `version` in, and
# helm-unittest validates the schema, so it cannot. These two guards cover what a
# render cannot: that both call sites exist, and that the table still means
# something for the defaults the chart ships.

NODES_TEMPLATE="$REPO_ROOT/packages/apps/kubernetes-nodes/templates/nodegroup.yaml"
NODES_SCHEMA="$REPO_ROOT/packages/apps/kubernetes-nodes/values.schema.json"

@test "the support matrix is consulted for the image-resolved Talos version, not only talos.version" {
  calls=$(grep -c 'include "kubernetes-nodes.assertTalosSupportsKubernetes"' "$NODES_TEMPLATE" || true)
  if [ "$calls" -lt 2 ]; then
    echo "nodegroup.yaml calls the Talos support-matrix guard $calls time(s); it needs one for talos.version and one for the version image.builtin/image.factory resolves to" >&2
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
  ver=$(yq '.talos.version' "$APP_VALUES")
  minor=$(printf '%s\n' "$ver" | grep -oE '^v[0-9]+\.[0-9]+')
  [ -n "$minor" ]

  helpers="$REPO_ROOT/packages/apps/kubernetes-nodes/templates/_helpers.tpl"
  # The row is a Helm dict literal: `"v1.13" (list "v1.31" "v1.32" ...)`.
  row=$(grep -oE "\"${minor}\" \(list [^)]*\)" "$helpers" || true)
  if [ -z "$row" ]; then
    echo "the Talos support matrix in $helpers has no row for ${minor}, the default talos.version ($ver)" >&2
    echo "an unlisted Talos minor passes the guard unchecked, so the guard means nothing on the shipped defaults" >&2
    return 1
  fi

  # Every version the schema lets an operator pick must be in that row, or the
  # guard rejects a combination the API advertises as valid.
  for k in $(yq -r '.properties.version.enum[]' "$NODES_SCHEMA"); do
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

# -----------------------------------------------------------------------------
# Third guard, and the premise the first one rests on. The catalog is keyed off
# the kubernetes-nodes chart's `talos` defaults, but the parent kubernetes chart
# carries its own copy of the same block since the Phase 2 split, and its own
# values.yaml says to keep the two in sync. Nothing enforced that: the pool
# chart's `make update` copies files/versions.yaml and images/kubectl.tag from
# the parent and not the talos block, so a one-sided bump leaves the parent's
# support-matrix guard reasoning about one Talos release while every worker boots
# another — and the catalog check above would go on passing, because it only ever
# reads one of the two files.

PARENT_VALUES="$REPO_ROOT/packages/apps/kubernetes/values.yaml"

@test "the parent kubernetes chart and kubernetes-nodes agree on their Talos defaults" {
  for key in version schematicID imageFactoryURL installerRepository registryMirrors; do
    parent=$(yq ".talos.${key}" "$PARENT_VALUES")
    pool=$(yq ".talos.${key}" "$APP_VALUES")
    if [ "$parent" != "$pool" ]; then
      echo "talos.${key} differs between the two charts:" >&2
      echo "  packages/apps/kubernetes/values.yaml:       $parent" >&2
      echo "  packages/apps/kubernetes-nodes/values.yaml: $pool" >&2
      echo "both are declared 'keep in sync' and a worker pool follows the second while the parent's support-matrix guard reads the first" >&2
      return 1
    fi
  done
}

# -----------------------------------------------------------------------------
# Fourth guard: the e2e suites must actually take the clone path.
#
# Enabling the catalog in the sandbox buys nothing unless the worker pool asks for
# it. If the `image` block is dropped from the KubernetesNodes heredoc, or lands at
# the wrong indent, the pool falls back to the HTTP import and every suite still
# passes -- the catalog imports its golden, nobody clones it, and the path this
# feature exists for ships with unit coverage alone. That silent-fallback shape is
# the one this branch already had to fix once, in the block that carried the
# registry mirror, so it gets a guard rather than a comment.

RUN_KUBERNETES_LIB="$REPO_ROOT/hack/e2e-chainsaw/_lib/run-kubernetes.sh"

@test "the e2e worker pool boots from the golden image rather than an HTTP import" {
  # Read the KubernetesNodes heredoc rather than the whole file: `image:` appears
  # in prose and in the Kubernetes CR too, and matching either would pass while the
  # pool imported over HTTP.
  block=$(awk '
    $0 == "kind: KubernetesNodes" { inblock = 1 }
    inblock && $0 == "EOF" { inblock = 0 }
    inblock { print }
  ' "$RUN_KUBERNETES_LIB")
  if [ -z "$block" ]; then
    echo "no KubernetesNodes heredoc found in $RUN_KUBERNETES_LIB; this guard has lost its subject" >&2
    return 1
  fi
  case "$block" in
    *"
  image:
    builtin: {}"*) ;;
    *)
      echo "the e2e KubernetesNodes pool does not set image.builtin at pool-value indent, so its workers import the OS image over HTTP" >&2
      echo "the kubernetes-worker-image catalog is then enabled in the sandbox and never exercised, and no test fails" >&2
      return 1 ;;
  esac

  # And the golden it clones is waited on before the pool is created, so a factory
  # stall is reported as itself rather than as a node-join timeout ten minutes later.
  if ! grep -q 'kubectl -n cozy-public get datavolume' "$RUN_KUBERNETES_LIB"; then
    echo "nothing waits for the golden import in $RUN_KUBERNETES_LIB before the pool is applied" >&2
    return 1
  fi
}
