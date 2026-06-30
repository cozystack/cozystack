#!/usr/bin/env bats
# Unit tests for hack/overlay-main-images.sh — the PR-finalize step that points
# packages a PR did NOT rebuild at the current-main images from the
# cozystack-packages:main artifact.
#
# Run from the repo root:  bats hack/overlay-main-images_test.bats
# (CI runs it via hack/cozytest.sh through `make unit-tests`.)
#
# Each test builds a throwaway tree: a `packages/` tree on release (ghcr/v1.5.0)
# refs, and a `main/` dir standing in for the extracted cozystack-packages:main
# artifact (root = contents of packages/) on current-main (OCIR/:main) refs.
# $root is the real repo, captured before cd.

@test "overlays an unbuilt unit (.tag) to current-main and reports it" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/apps/foo/images" "$w/main/apps/foo/images"
  echo 'ghcr.io/cozystack/cozystack/foo:v1.5.0@sha256:aaaa' > "$w/packages/apps/foo/images/foo.tag"
  echo 'iad.ocir.io/x/cozystack/foo:main@sha256:bbbb'       > "$w/main/apps/foo/images/foo.tag"
  cd "$w"
  out=$("$root/hack/overlay-main-images.sh" main '[]')
  grep -q 'foo:main@sha256:bbbb' packages/apps/foo/images/foo.tag
  echo "$out" | grep -q 'overlaid=1'
}

@test "overlays a split-form ref (repository/tag/digest, no @sha256 on those lines)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/system/split" "$w/main/system/split"
  printf 'image:\n  repository: ghcr.io/cozystack/cozystack/split\n  tag: v1.5.0\n  digest: "sha256:aaaa"\n' > "$w/packages/system/split/values.yaml"
  printf 'image:\n  repository: iad.ocir.io/x/cozystack/split\n  tag: main\n  digest: "sha256:bbbb"\n'      > "$w/main/system/split/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'repository: iad.ocir.io/x/cozystack/split' packages/system/split/values.yaml
  grep -q 'tag: main' packages/system/split/values.yaml
  grep -q 'sha256:bbbb' packages/system/split/values.yaml
}

@test "overlays an extra/* unit (outside the per-package build matrix)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/extra/seaweedfs/images" "$w/main/extra/seaweedfs/images"
  echo 'ghcr.io/cozystack/cozystack/objectstorage-sidecar:v1.5.0@sha256:aaaa' > "$w/packages/extra/seaweedfs/images/objectstorage-sidecar.tag"
  echo 'iad.ocir.io/x/cozystack/objectstorage-sidecar:main@sha256:bbbb'       > "$w/main/extra/seaweedfs/images/objectstorage-sidecar.tag"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'objectstorage-sidecar:main@sha256:bbbb' packages/extra/seaweedfs/images/objectstorage-sidecar.tag
}

@test "skips a unit the PR rebuilt (its pr-<N>-<sha> ref wins)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/apps/foo" "$w/main/apps/foo"
  echo 'image: ghcr.io/cozystack/cozystack/foo:v1.5.0@sha256:aaaa' > "$w/packages/apps/foo/values.yaml"
  echo 'image: iad.ocir.io/x/cozystack/foo:main@sha256:bbbb'       > "$w/main/apps/foo/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '["packages/apps/foo"]'
  grep -q 'foo:v1.5.0@sha256:aaaa' packages/apps/foo/values.yaml
}

@test "never overlays core/talos or core/installer (owned by dedicated jobs)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/core/installer" "$w/main/core/installer" "$w/packages/core/talos" "$w/main/core/talos"
  echo 'image: ghcr.io/cozystack/cozystack/cozystack-operator:v1.5.0@sha256:aaaa' > "$w/packages/core/installer/values.yaml"
  echo 'image: iad.ocir.io/x/cozystack/cozystack-operator:main@sha256:bbbb'       > "$w/main/core/installer/values.yaml"
  echo 'image: ghcr.io/cozystack/cozystack/talos:v1.5.0@sha256:cccc' > "$w/packages/core/talos/values.yaml"
  echo 'image: iad.ocir.io/x/cozystack/talos:main@sha256:dddd'       > "$w/main/core/talos/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'cozystack-operator:v1.5.0@sha256:aaaa' packages/core/installer/values.yaml
  grep -q 'talos:v1.5.0@sha256:cccc' packages/core/talos/values.yaml
}

@test "does not descend into vendored charts/ subtrees" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/system/bar/charts/sub" "$w/main/system/bar/charts/sub"
  echo 'image: ghcr.io/cozystack/cozystack/sub:v1.5.0@sha256:aaaa' > "$w/packages/system/bar/charts/sub/values.yaml"
  echo 'image: iad.ocir.io/x/cozystack/sub:main@sha256:bbbb'       > "$w/main/system/bar/charts/sub/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'sub:v1.5.0@sha256:aaaa' packages/system/bar/charts/sub/values.yaml
}

@test "keeps the committed ref when a non-ref line differs (drift)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/system/drift" "$w/main/system/drift"
  printf 'tuning: old\nimage: ghcr.io/cozystack/cozystack/drift:v1.5.0@sha256:aaaa\n' > "$w/packages/system/drift/values.yaml"
  printf 'tuning: new\nimage: iad.ocir.io/x/cozystack/drift:main@sha256:bbbb\n'        > "$w/main/system/drift/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'drift:v1.5.0@sha256:aaaa' packages/system/drift/values.yaml
  grep -q 'tuning: old' packages/system/drift/values.yaml
}

@test "missing artifact directory is a no-op and exits 0" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/apps/foo"
  echo 'image: ghcr.io/cozystack/cozystack/foo:v1.5.0@sha256:aaaa' > "$w/packages/apps/foo/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" does-not-exist '[]'
  grep -q 'foo:v1.5.0@sha256:aaaa' packages/apps/foo/values.yaml
}
