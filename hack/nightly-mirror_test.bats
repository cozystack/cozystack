#!/usr/bin/env bats
# Tests for hack/nightly-mirror.sh — the OCIR->GHCR nightly image-mirror selector.
#
# Guards the ref selection and host rewrite: only cozystack-owned component
# images are copied (third-party images and bare upstream tags are skipped, as
# they live in registries this job cannot push to), the cozystack-packages
# artifact is deliberately excluded (it is rebuilt downstream from the rewritten
# tree), and every copy targets the destination registry with the source digest
# preserved.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats — see the same
# note in hack/promote-retag_test.bats. No `run`, `$status`, `$output`, `skip`,
# or setup()/teardown(); each @test is a shell function under `set -eu -x`, so a
# non-zero exit aborts the test (that is the exit-0 assertion). A test that
# expects a non-zero exit must capture it with `|| rc=$?`. mikefarah yq is
# assumed present (provided by the test toolchain).
#
# Run with: hack/cozytest.sh hack/nightly-mirror_test.bats

# Build a synthetic baked tree exercising all three image-ref shapes plus the
# refs that MUST be filtered (third-party host, cozystack-packages artifact).
_make_tree() {
  D="$(printf 'a%.0s' $(seq 1 64))"   # 64-hex fake digest body
  t="$1"
  mkdir -p "$t/system/foo" "$t/system/bar" "$t/system/third" "$t/core/installer"
  # shape 1: single string, cozystack-owned -> copied
  printf 'image: iad.ocir.io/idyksih5sir9/cozystack/foo:main@sha256:%s\n' "$D" > "$t/system/foo/values.yaml"
  # shape 2: split map, cozystack-owned -> copied
  {
    echo 'image:'
    echo '  repository: iad.ocir.io/idyksih5sir9/cozystack/bar'
    echo '  tag: main'
    printf '  digest: sha256:%s\n' "$D"
  } > "$t/system/bar/values.yaml"
  # third-party single string -> skipped
  printf 'image: docker.io/clastix/kubectl:1.0@sha256:%s\n' "$D" > "$t/system/third/values.yaml"
  # shape 3: operator (cozystack-owned -> copied) + platformSource (cozystack-packages -> skipped)
  {
    echo 'cozystackOperator:'
    printf '  image: iad.ocir.io/idyksih5sir9/cozystack/cozystack-operator:main@sha256:%s\n' "$D"
    echo '  platformSourceUrl: oci://iad.ocir.io/idyksih5sir9/cozystack/cozystack-packages'
    printf '  platformSourceRef: "digest=sha256:%s"\n' "$D"
  } > "$t/core/installer/values.yaml"
}

@test "dry-run mirrors only cozystack-owned component images to the dest registry" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_tree "$tmp/tree"

  rc=0
  hack/nightly-mirror.sh 0.0.0-nightly.test "$tmp/tree" --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "nightly-mirror.sh exited $rc; yq: $(yq --version 2>&1)" >&2
    echo "--- stderr ---" >&2; cat "$tmp/err" >&2
    echo "--- stdout ---" >&2; cat "$tmp/out" >&2
    return "$rc"
  fi

  # The three cozystack-owned component images are each copied to GHCR by digest.
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/foo@sha256:.* docker://ghcr.io/cozystack/cozystack/foo:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://ghcr.io/cozystack/cozystack/bar:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://ghcr.io/cozystack/cozystack/cozystack-operator:0.0.0-nightly.test' "$tmp/out"
  # A floating tag is moved alongside the pinned version.
  grep -q 'docker://ghcr.io/cozystack/cozystack/foo:nightly' "$tmp/out"

  # Third-party images never appear in the copy plan.
  ! grep -q 'docker.io/clastix' "$tmp/out"
  # The cozystack-packages artifact is excluded — it is rebuilt downstream.
  ! grep -qE 'skopeo copy.*cozystack-packages' "$tmp/out"

  # The host rewrite is planned source->dest.
  grep -q "s|iad.ocir.io/idyksih5sir9/cozystack/|ghcr.io/cozystack/cozystack/|g" "$tmp/out"
}

@test "empty selection (wrong source registry) exits non-zero with a diagnostic" {
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  _make_tree "$tmp/tree"

  # No images live under example.com/nope, so nothing is selected and the script
  # exits non-zero rather than silently mirroring an empty set.
  rc=0
  SRC_REGISTRY="example.com/nope" hack/nightly-mirror.sh 0.0.0-nightly.test "$tmp/tree" --dry-run \
    >"$tmp/out" 2>"$tmp/err" || rc=$?

  [ "$rc" -ne 0 ]
  grep -q 'No cozystack-owned digest-pinned image refs found' "$tmp/err"
}
