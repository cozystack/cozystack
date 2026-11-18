#!/usr/bin/env bats
# Tests for hack/retention-packages.sh — the package-name source of the GHCR
# retention workflow. GitHub refuses to list org container packages to an app
# installation token, so the workflow sweeps exactly the packages this script
# names. A name missing here is a package whose nightlies are never pruned, so
# the tests pin what is included and that an unusable tree fails rather than
# yielding a shorter list.
#
# Run with: bats hack/retention-packages_test.bats

load test_helper

SCRIPT="${BATS_TEST_DIRNAME}/retention-packages.sh"

# A published nightly tree: refs already rewritten to the GHCR host.
_make_tree() {
  D="$(printf 'a%.0s' $(seq 1 64))"
  t="$1"
  mkdir -p "$t/core/installer" "$t/system/single" "$t/system/split" \
           "$t/system/nested" "$t/system/third" "$t/system/leftover" \
           "$t/system/tagfile/images" "$t/system/multus/templates"
  {
    echo 'cozystackOperator:'
    echo '  platformSourceUrl: oci://ghcr.io/cozystack/cozystack/cozystack-packages'
    printf '  platformSourceRef: digest=sha256:%s\n' "$D"
  } > "$t/core/installer/values.yaml"
  printf 'image: ghcr.io/cozystack/cozystack/single:v1.0.0@sha256:%s\n' "$D" \
    > "$t/system/single/values.yaml"
  {
    echo 'image:'
    echo '  repository: ghcr.io/cozystack/cozystack/split'
    printf '  tag: v1.0.0@sha256:%s\n' "$D"
  } > "$t/system/split/values.yaml"
  printf 'image: ghcr.io/cozystack/cozystack/metallb/controller:v1@sha256:%s\n' "$D" \
    > "$t/system/nested/values.yaml"
  printf 'ghcr.io/cozystack/cozystack/tagfile:v1.0.0@sha256:%s\n' "$D" \
    > "$t/system/tagfile/images/thing.tag"
  printf '          image: ghcr.io/cozystack/cozystack/multus-cni:v1@sha256:%s\n' "$D" \
    > "$t/system/multus/templates/multus-daemonset-thick.yml"
  # Not ours to prune: a third-party image and a sibling GHCR namespace.
  {
    printf 'a: docker.io/library/busybox:1.37.0@sha256:%s\n' "$D"
    printf 'b: ghcr.io/cozystack/ingress-nginx-with-protobuf-exporter/controller:v1@sha256:%s\n' "$D"
  } > "$t/system/third/values.yaml"
  # A ref the mirror failed to rewrite lives in the build registry, not GHCR.
  printf 'image: iad.ocir.io/idyksih5sir9/cozystack/leftover:main@sha256:%s\n' "$D" \
    > "$t/system/leftover/values.yaml"
}

@test "names every GHCR cozystack package in all storage shapes plus the published extras" {
  tree="${BATS_TEST_TMPDIR}/tree"
  _make_tree "$tree"
  run "$SCRIPT" "$tree"
  [ "$status" -eq 0 ]
  expected="cozystack/cozy-installer
cozystack/cozystack-nocloud
cozystack/cozystack-packages
cozystack/metallb/controller
cozystack/multus-cni
cozystack/single
cozystack/split
cozystack/tagfile"
  [ "$output" = "$expected" ]
}

@test "fails on a missing tree" {
  run "$SCRIPT" "${BATS_TEST_TMPDIR}/absent"
  [ "$status" -ne 0 ]
}

@test "fails on a tree that does not reference the packages artifact" {
  tree="${BATS_TEST_TMPDIR}/tree"
  _make_tree "$tree"
  rm "$tree/core/installer/values.yaml"
  run "$SCRIPT" "$tree"
  [ "$status" -ne 0 ]
  case "$output" in
    *cozystack/single*) echo "FAIL: names printed despite failure"; false ;;
  esac
}
