#!/usr/bin/env bats
# The ComputePlane admin-kubeconfig Secret name derives from the cluster
# HelmRelease object name (extra/computeplane/templates/cluster.yaml sets no
# spec.releaseName), while computeplane-rd withholds that Secret from
# tenant-visible secrets by literal name under secrets.exclude. Rename the
# object without the exclusion following and the tenant regains the
# cluster-admin credential the whole design exists to withhold. This test
# pins the pair so a future rename fails here instead of silently re-exposing
# the credential.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "computeplane-rd excludes the admin kubeconfig Secret the cluster release actually produces" {
  command -v helm >/dev/null || { echo "helm is required" >&2; exit 1; }
  command -v yq >/dev/null || { echo "yq (mikefarah v4+) is required" >&2; exit 1; }

  rendered="$(helm template computeplane "$REPO_ROOT/packages/extra/computeplane")"

  # Release name of the cluster HelmRelease: spec.releaseName when set, else
  # the object name (Flux's default with no targetNamespace).
  release="$(printf '%s' "$rendered" | yq eval-all 'select(.kind == "HelmRelease") | .spec.releaseName // .metadata.name' -)"
  [ -n "$release" ]
  [ "$release" != "null" ]

  expected="${release}-admin-kubeconfig"
  excluded="$(yq '.spec.secrets.exclude[].resourceNames[]' "$REPO_ROOT/packages/system/computeplane-rd/cozyrds/computeplane.yaml")"

  echo "expected exclusion: $expected"
  echo "declared exclusions: $excluded"
  printf '%s\n' "$excluded" | grep -qx "$expected"
}
