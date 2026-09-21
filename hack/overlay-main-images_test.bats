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

# A drifted file keeps its committed release ref, which is the safe choice, but
# it also means that component runs a release image in a lane meant to exercise
# the tree under review. That used to be one line among hundreds, so three
# separate PRs each spent a round establishing that a red backup suite was not
# theirs (#4257).
# A ref whose key merely ENDS in Image, and which carries no digest to fall back
# on, used to read as a config change and cost its whole file the overlay. That
# is how backupstrategy-controller ran a release image two months older than the
# tree in every lane that did not rebuild it (#4257): one such line sat beside
# digest-pinned siblings the classifier did recognise.
@test "a suffixed image key is a ref, not a config change" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/gamma" "$w/main/system/gamma"
  printf 'gamma:\n  image: "ghcr.io/cozystack/cozystack/gamma:v1.6.0@sha256:aaaa"\n  clientImage: "ghcr.io/cozystack/cozystack/client:v1.6.0"\n' > "$w/packages/system/gamma/values.yaml"
  printf 'gamma:\n  image: "iad.ocir.io/x/cozystack/gamma:main@sha256:cccc"\n  clientImage: "iad.ocir.io/x/cozystack/client:main"\n' > "$w/main/system/gamma/values.yaml"
  ( cd "$w" && "$root/hack/overlay-main-images.sh" main '[]' ) > "$w/out.txt"
  grep -q 'overlay: packages/system/gamma/values.yaml' "$w/out.txt"
  grep -q 'iad.ocir.io/x/cozystack/client:main' "$w/packages/system/gamma/values.yaml"
  rm -rf "$w"
}

# A file differing on imagePullPolicy differs on CONFIG, so it must keep its
# committed ref rather than be overlaid. What keeps both pull-policy keys out is
# the letter the key branch demands BEFORE `image`, which a key beginning with
# `image` has nothing to supply: neither matches with the trailing colon or
# without it, even when the value carries a slash. The colon belongs to the
# other side of the branch and is held by "a suffixed image key is a ref".
@test "a key merely STARTING with image is config, not a ref" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/delta" "$w/main/system/delta"
  printf 'delta:\n  image: "ghcr.io/cozystack/cozystack/delta:v1.6.0@sha256:aaaa"\n  imagePullPolicy: IfNotPresent\n' > "$w/packages/system/delta/values.yaml"
  printf 'delta:\n  image: "ghcr.io/cozystack/cozystack/delta:v1.6.0@sha256:aaaa"\n  imagePullPolicy: Always\n' > "$w/main/system/delta/values.yaml"
  ( cd "$w" && "$root/hack/overlay-main-images.sh" main '[]' ) > "$w/out.txt"
  grep -q 'drift (non-ref change) in packages/system/delta/values.yaml' "$w/out.txt"
  if grep -q 'overlay: packages/system/delta/values.yaml' "$w/out.txt"; then
    echo "FAIL: a changed imagePullPolicy was read as a ref and the file overlaid" >&2
    cat "$w/out.txt" >&2
    exit 1
  fi
  grep -q 'imagePullPolicy: IfNotPresent' "$w/packages/system/delta/values.yaml"
  rm -rf "$w"
}

# `yq -i` re-emits the document when the build stamps a digest, and a blank
# line separating two top-level keys does not survive that. So the artifact copy
# of a file the PR never touched differs from the committed one by a line
# carrying no configuration, and treating that as a config change costs the file
# its overlay. Five packages drifted on nothing else (#4265).
@test "a blank line lost to yq is not a config change" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/epsilon" "$w/main/system/epsilon"
  printf '_cluster: {}\n\nepsilon:\n  image: "ghcr.io/cozystack/cozystack/epsilon:v1.6.0@sha256:aaaa"\n' > "$w/packages/system/epsilon/values.yaml"
  printf '_cluster: {}\nepsilon:\n  image: "iad.ocir.io/x/cozystack/epsilon:main@sha256:bbbb"\n' > "$w/main/system/epsilon/values.yaml"
  ( cd "$w" && "$root/hack/overlay-main-images.sh" main '[]' ) > "$w/out.txt"
  grep -q 'overlay: packages/system/epsilon/values.yaml' "$w/out.txt"
  if grep -q 'drift (non-ref change)' "$w/out.txt"; then
    echo "FAIL: a blank line lost to yq was counted as a config change" >&2
    cat "$w/out.txt" >&2
    exit 1
  fi
  grep -q 'iad.ocir.io/x/cozystack/epsilon:main' "$w/packages/system/epsilon/values.yaml"
  rm -rf "$w"
}

# A blank line is discounted, a line with CONTENT is not: the guard that keeps
# a real config difference from riding along with the ref lines is the same one,
# so it needs its own case rather than trusting the blank-line test above.
@test "a real config line still counts as drift alongside a blank line" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/zeta" "$w/main/system/zeta"
  printf '_cluster: {}\n\nzeta:\n  image: "ghcr.io/cozystack/cozystack/zeta:v1.6.0@sha256:aaaa"\n  replicas: 2\n' > "$w/packages/system/zeta/values.yaml"
  printf '_cluster: {}\nzeta:\n  image: "iad.ocir.io/x/cozystack/zeta:main@sha256:bbbb"\n  replicas: 3\n' > "$w/main/system/zeta/values.yaml"
  ( cd "$w" && "$root/hack/overlay-main-images.sh" main '[]' ) > "$w/out.txt"
  grep -q 'drift (non-ref change) in packages/system/zeta/values.yaml' "$w/out.txt"
  grep -q 'replicas: 2' "$w/packages/system/zeta/values.yaml"
  rm -rf "$w"
}

@test "drift is summarised as a GitHub annotation naming the packages" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/alpha" "$w/main/system/alpha"
  mkdir -p "$w/packages/system/beta" "$w/main/system/beta"
  printf 'tuning: old\nimage: ghcr.io/cozystack/cozystack/alpha:v1.5.0@sha256:aaaa\n' > "$w/packages/system/alpha/values.yaml"
  printf 'tuning: new\nimage: iad.ocir.io/x/cozystack/alpha:main@sha256:bbbb\n'        > "$w/main/system/alpha/values.yaml"
  printf 'tuning: old\nimage: ghcr.io/cozystack/cozystack/beta:v1.5.0@sha256:cccc\n'  > "$w/packages/system/beta/values.yaml"
  printf 'tuning: new\nimage: iad.ocir.io/x/cozystack/beta:main@sha256:dddd\n'         > "$w/main/system/beta/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]' > out.txt
  # Read the names out of the ANNOTATION line, not the whole log: every drifted
  # file already prints its own path above, so grepping the log would pass even
  # if the summary named nothing.
  grep '::warning title=Image overlay drift' out.txt > ann.txt
  grep -q 'system/alpha' ann.txt
  grep -q 'system/beta' ann.txt
  grep -q '2 package(s)' ann.txt
  cd "$root"
  rm -rf "$w"
}

@test "no annotation when nothing drifted" {
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/clean" "$w/main/system/clean"
  printf 'image: ghcr.io/cozystack/cozystack/clean:v1.5.0@sha256:aaaa\n' > "$w/packages/system/clean/values.yaml"
  printf 'image: iad.ocir.io/x/cozystack/clean:main@sha256:bbbb\n'       > "$w/main/system/clean/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]' > out.txt
  if grep -q '::warning title=Image overlay drift' out.txt; then
    echo "FAIL: the annotation was emitted on a clean run" >&2
    cat out.txt >&2
    exit 1
  fi
  cd "$root"
  rm -rf "$w"
}

@test "two drifted files in one package are one package, named without /images" {
  # The counter counts ref-bearing FILES; the annotation speaks of packages. A
  # package whose values.yaml and images/*.tag both drift is still one package,
  # and `<pkg>/images` is not something anyone can go and look at.
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/apps/omega/images" "$w/main/apps/omega/images"
  printf 'image: ghcr.io/cozystack/cozystack/omega:v1.5.0@sha256:aaaa\nreplicas: 1\n' \
    > "$w/packages/apps/omega/values.yaml"
  printf 'image: iad.ocir.io/x/cozystack/omega:main@sha256:bbbb\nreplicas: 2\n' \
    > "$w/main/apps/omega/values.yaml"
  printf 'ghcr.io/cozystack/cozystack/t:v1.5.0@sha256:cccc\n' \
    > "$w/packages/apps/omega/images/t.tag"
  printf 'iad.ocir.io/x/cozystack/t:main@sha256:dddd\ntrailing\n' \
    > "$w/main/apps/omega/images/t.tag"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]' > out.txt
  ann=$(grep '::warning title=Image overlay drift' out.txt)
  if ! printf '%s\n' "$ann" | grep -q '1 package(s)'; then
    echo "FAIL: two files in one package were not counted as one package" >&2
    printf '%s\n' "$ann" >&2
    exit 1
  fi
  if printf '%s\n' "$ann" | grep -q 'omega/images'; then
    echo "FAIL: the annotation named a directory instead of the package" >&2
    printf '%s\n' "$ann" >&2
    exit 1
  fi
  cd "$root"
  rm -rf "$w"
}

@test "an *Image key holding operator config, not a path, is still a config change" {
  # `vddkImage: ""` (core/platform, migration-controller) is named after an
  # image but is an operator-supplied knob the build never stamps. The prefixed
  # -key branch must not absorb it: taking it from the artifact in silence is
  # the failure this script exists to make loud.
  root=$(pwd)
  w=$(mktemp -d)
  mkdir -p "$w/packages/system/vm" "$w/main/system/vm"
  printf 'image: ghcr.io/cozystack/cozystack/vm:v1.5.0@sha256:aaaa\nvddkImage: ""\n' \
    > "$w/packages/system/vm/values.yaml"
  printf 'image: iad.ocir.io/x/cozystack/vm:main@sha256:bbbb\nvddkImage: "someone/else"\n' \
    > "$w/main/system/vm/values.yaml"
  cd "$w"
  "$root/hack/overlay-main-images.sh" main '[]' > out.txt
  if ! grep -q 'drift (non-ref change) in packages/system/vm/values.yaml' out.txt; then
    echo "FAIL: a changed vddkImage was taken from the artifact instead of drifting" >&2
    cat out.txt >&2
    exit 1
  fi
  cd "$root"
  rm -rf "$w"
}
