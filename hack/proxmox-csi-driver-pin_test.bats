#!/usr/bin/env bats
# Upstream releases the Proxmox CSI controller and node plugin together, and
# its chart renders both from one appVersion. Here they run in two places and
# are pinned in two packages: the controller in the management cluster
# (packages/apps/kubernetes, an images/.tag file) and the node plugin in the
# tenant (packages/system/proxmox-csi, a values override on the vendored
# chart). Both are pinned by digest because tags in that registry have been
# re-pushed, and the pin is what takes the version out of the chart's hands:
# `make update` with a new CHART_VERSION brings new templates and a new
# appVersion while both pins keep naming the old build, and nothing else
# notices.
#
# This asserts the chart's appVersion and both pins name the same release, and
# that both pins carry a digest.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

DIGEST_RE='@sha256:[0-9a-f]{64}$'

@test "the Proxmox CSI controller and node plugin pin the vendored chart's release by digest" {
  chart="$REPO_ROOT/packages/system/proxmox-csi/charts/proxmox-csi-plugin/Chart.yaml"
  values="$REPO_ROOT/packages/system/proxmox-csi/values.yaml"
  tagfile="$REPO_ROOT/packages/apps/kubernetes/images/proxmox-csi-controller.tag"

  app_version=$(yq -r '.appVersion' "$chart")
  node_tag=$(yq -r '."proxmox-csi-plugin".node.plugin.image.tag' "$values")
  controller_ref=$(tr -d '[:space:]' < "$tagfile")

  [ -n "$app_version" ] && [ "$app_version" != null ] || {
    echo "no appVersion in $chart" >&2
    return 1
  }

  printf '%s\n' "$node_tag" | grep -Eq "^${app_version//./\\.}${DIGEST_RE}" || {
    echo "node plugin tag '$node_tag' is not the chart appVersion $app_version with a digest" >&2
    echo "set proxmox-csi-plugin.node.plugin.image.tag in packages/system/proxmox-csi/values.yaml" >&2
    return 1
  }

  printf '%s\n' "$controller_ref" | grep -Eq "^ghcr\.io/sergelogvinov/proxmox-csi-controller:${app_version//./\\.}${DIGEST_RE}" || {
    echo "controller ref '$controller_ref' is not proxmox-csi-controller:$app_version with a digest" >&2
    echo "update packages/apps/kubernetes/images/proxmox-csi-controller.tag" >&2
    return 1
  }
}
