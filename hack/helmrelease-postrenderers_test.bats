#!/usr/bin/env bats
# The platform's image override table (registries.imageOverrides, carried to
# every release as _cluster.image-overrides) reaches the HelmReleases built in
# Go through pkg/imageoverrides. A HelmRelease a chart renders is built by
# nobody else: unless its template calls cozy-lib.postRenderers, the images it
# installs keep their upstream names, and on an air-gapped cluster serving a
# renamed image set they never pull. Nothing fails at render time when a call
# is missing, so the first check walks every HelmRelease template in the tree.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "every HelmRelease a chart renders calls cozy-lib.postRenderers" {
  local missing="" file releases calls
  # Vendored upstream charts under charts/ are not ours to change and render
  # no Cozystack HelmReleases.
  for file in $(grep -rl --include='*.yaml' --include='*.yml' '^kind: HelmRelease' "$REPO_ROOT"/packages/*/*/templates); do
    releases=$(grep -c '^kind: HelmRelease' "$file")
    calls=$(grep -c 'include "cozy-lib.postRenderers"' "$file" || true)
    if [ "$releases" -ne "$calls" ]; then
      missing="$missing ${file#"$REPO_ROOT"/} ($releases HelmRelease, $calls calls)"
    fi
  done
  if [ -n "$missing" ]; then
    echo "HelmRelease templates without cozy-lib.postRenderers:$missing" >&2
    return 1
  fi
}

@test "a package that renders a HelmRelease vendors cozy-lib" {
  local missing="" pkg
  for pkg in $(grep -rl '^kind: HelmRelease' "$REPO_ROOT"/packages/*/*/templates | sed 's|/templates/.*||' | sort -u); do
    [ -e "$pkg/charts/cozy-lib/Chart.yaml" ] || missing="$missing ${pkg#"$REPO_ROOT"/}"
  done
  if [ -n "$missing" ]; then
    echo "packages without charts/cozy-lib:$missing" >&2
    return 1
  fi
}

@test "a tenant's module HelmReleases carry the override table" {
  local rendered got
  rendered="$BATS_TEST_TMPDIR/tenant.yaml"
  helm template tenant-foo "$REPO_ROOT/packages/apps/tenant" --namespace tenant-root --set etcd=true \
    --set-json '_cluster={"image-overrides":[{"name":"ghcr.io/cozystack/cozystack/etcd","newName":"registry.example.org/os/etcd","newTag":"3.6.11"}]}' \
    > "$rendered"
  got=$(yq -N -o json -I0 'select(.kind == "HelmRelease" and .metadata.name == "etcd") | .spec.postRenderers' "$rendered")
  [ "$got" = '[{"kustomize":{"images":[{"name":"ghcr.io/cozystack/cozystack/etcd","newName":"registry.example.org/os/etcd","newTag":"3.6.11"}]}}]' ] || {
    echo "etcd HelmRelease postRenderers: $got" >&2
    return 1
  }
}

@test "without an override table a chart-rendered HelmRelease has no postRenderers" {
  local rendered got
  rendered="$BATS_TEST_TMPDIR/tenant.yaml"
  helm template tenant-foo "$REPO_ROOT/packages/apps/tenant" --namespace tenant-root --set etcd=true > "$rendered"
  got=$(yq -N 'select(.kind == "HelmRelease" and .metadata.name == "etcd") | .spec | has("postRenderers")' "$rendered")
  [ "$got" = "false" ] || { echo "etcd HelmRelease has postRenderers without a table" >&2; return 1; }
}
