#!/usr/bin/env bats
# EXIT-TRAP DEBT: 11 -- see hack/bats-no-exit-trap.bats; lower it as the traps go, delete it at zero.
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

@test "preserves a PR-edited non-rebuilt package ref (passed via the touched arg)" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/system/keycloak" "$w/main/system/keycloak"
  # The PR bumps an upstream image in keycloak. keycloak is NOT a build unit, so
  # it is absent from BUILD_MATRIX; only its image line differs from the artifact.
  # Without the touched arg the overlay would silently revert the PR's bump.
  echo 'image: quay.io/keycloak/keycloak:26.7.0' > "$w/packages/system/keycloak/values.yaml"
  echo 'image: quay.io/keycloak/keycloak:26.6.3' > "$w/main/system/keycloak/values.yaml"
  cd "$w"
  out=$("$root/hack/overlay-main-images.sh" main '[]' 'packages/system/keycloak')
  # The PR's edit wins; the overlay must not touch it.
  grep -q 'keycloak:26.7.0' packages/system/keycloak/values.yaml
  echo "$out" | grep -q 'overlaid=0'
}

@test "recognizes a --…-image= arg line (no @sha256, no image key) as an image ref" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/system/argimg" "$w/main/system/argimg"
  # The only differing line is a `--…-image=` arg with neither @sha256 nor an
  # image/tag/repository key — it must still count as image-reference-bearing.
  printf 'args:\n  - --provider-image=ghcr.io/cozystack/cozystack/x:v1.5.0\n' > "$w/packages/system/argimg/values.yaml"
  printf 'args:\n  - --provider-image=iad.ocir.io/x/cozystack/x:main\n'       > "$w/main/system/argimg/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'x:main' packages/system/argimg/values.yaml
}

@test "does not introduce a file present in the artifact but absent in the PR tree" {
  root=$(pwd)
  w=$(mktemp -d); trap 'rm -rf "$w"' EXIT
  mkdir -p "$w/packages/apps/present/images" "$w/main/apps/present/images" "$w/main/apps/ghost/images"
  echo 'ghcr.io/cozystack/cozystack/present:v1.5.0@sha256:aaaa' > "$w/packages/apps/present/images/present.tag"
  echo 'iad.ocir.io/x/cozystack/present:main@sha256:bbbb'       > "$w/main/apps/present/images/present.tag"
  # 'ghost' exists only in the artifact tree, never in the PR's packages/.
  echo 'iad.ocir.io/x/cozystack/ghost:main@sha256:cccc'         > "$w/main/apps/ghost/images/ghost.tag"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]'
  grep -q 'present:main@sha256:bbbb' packages/apps/present/images/present.tag
  [ ! -e packages/apps/ghost/images/ghost.tag ]
}

# ── workflow wiring ─────────────────────────────────────────────────────────
# The script is only half the mechanism; WHICH artifact the workflow hands it
# decides which generation of images a PR gets. A release-line PR must read its
# own line's artifact — reading main's puts main's binaries against that line's
# charts, which failed install deterministically (#3437).

@test "the overlay reads the artifact for the PR's own base branch" {
  root=$(pwd)
  wf="$root/.github/workflows/pull-requests.yaml"
  [ -f "$wf" ]

  # Executable lines only: a comment mentioning the tag must not satisfy this.
  code="$(grep -v '^[[:space:]]*#' "$wf")"

  block="$(printf '%s\n' "$code" | awk '
    $0 == "      - name: Pull base-branch packages tree" { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside')"
  [ -n "$block" ] || { echo "the base-branch packages pull step is missing from $wf" >&2; exit 1; }

  # The artifact tag must come from the base branch, passed via env.
  printf '%s\n' "$block" | grep -qF 'cozystack-packages:${BASE_REF}' || {
    echo "the packages artifact tag is not the PR's base branch. Reading a fixed" >&2
    echo "tag (e.g. :main) hands a release-line PR another generation's images." >&2
    exit 1; }
  printf '%s\n' "$block" | grep -qF 'BASE_REF: ${{ github.base_ref }}' || {
    echo "BASE_REF is not wired to github.base_ref in the pull step's env." >&2; exit 1; }

  # A hardcoded :main anywhere in the two overlay steps defeats the point.
  overlay="$(printf '%s\n' "$code" | awk '
    $0 == "      - name: Overlay base-branch refs for unbuilt packages" { inside = 1; next }
    /^      - name: / { inside = 0 }
    inside')"
  [ -n "$overlay" ] || { echo "the overlay step is missing from $wf" >&2; exit 1; }
  printf '%s\n%s\n' "$block" "$overlay" | grep -qF 'cozystack-packages:main' && {
    echo "an overlay step still pins cozystack-packages:main" >&2; exit 1; }
  return 0
}

@test "every maintained release line publishes its own packages artifact" {
  root=$(pwd)
  wf="$root/.github/workflows/build-release.yaml"
  [ -f "$wf" ] || {
    echo "build-release.yaml is missing: without a per-line artifact the overlay" >&2
    echo "above degrades to a no-op on release branches, so their PRs test the" >&2
    echo "line's last release rather than its tip." >&2
    exit 1; }

  code="$(grep -v '^[[:space:]]*#' "$wf")"

  # Line branches only. The per-release and rc staging branches promote-rc.yaml
  # and tags.yaml create (release-1.6.1, release-1.6.0-rc.4) must NOT trigger a
  # full rebuild — their images come from the tag build.
  printf '%s\n' "$code" | grep -qF "branches: ['release-[0-9]+.[0-9]+']" || {
    echo "build-release.yaml does not restrict its trigger to release-<major>.<minor>" >&2; exit 1; }

  # The artifact and image tag must be the branch, or the overlay cannot find it.
  printf '%s\n' "$code" | grep -qF 'IMAGE_TAG: ${{ github.ref_name }}' || {
    echo "build-release.yaml does not tag images/artifact with the branch name" >&2; exit 1; }

  # It must not write the shared mode=max cache: that ref has exactly one
  # serialized writer (build-main.yaml) so concurrent builds cannot race on the
  # cache manifest. A line build can overlap a main build.
  printf '%s\n' "$code" | grep -qF "WRITE_CACHE: '0'" || {
    echo "build-release.yaml must set WRITE_CACHE: '0' — writing the shared" >&2
    echo ":buildcache ref races build-main.yaml on the cache manifest." >&2
    exit 1; }
}
