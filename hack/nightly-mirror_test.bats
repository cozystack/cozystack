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

# Build a synthetic baked tree exercising all four image-ref shapes plus the
# refs that MUST be filtered (third-party host, cozystack-packages artifact).
_make_tree() {
  D="$(printf 'a%.0s' $(seq 1 64))"   # 64-hex fake digest body
  t="$1"
  mkdir -p "$t/system/foo" "$t/system/bar" "$t/system/split" "$t/system/third" \
           "$t/system/splithost" "$t/system/digesthost" "$t/system/globalreg" "$t/core/installer"
  # shape 1: single string, cozystack-owned -> copied
  printf 'image: iad.ocir.io/idyksih5sir9/cozystack/foo:main@sha256:%s\n' "$D" > "$t/system/foo/values.yaml"
  # shape 2: split map, cozystack-owned -> copied
  {
    echo 'image:'
    echo '  repository: iad.ocir.io/idyksih5sir9/cozystack/bar'
    echo '  tag: main'
    printf '  digest: sha256:%s\n' "$D"
  } > "$t/system/bar/values.yaml"
  # shape 3: split map with the digest embedded in `tag`, cozystack-owned -> copied.
  # This is what most package Makefiles write (a single yq call setting .image.tag
  # to "$(IMAGE_TAG)@$(digest)"), so it is the dominant real-world shape — linstor,
  # kamaji, kilo, metallb and redis-operator all use it. It matches neither shape 1
  # (rule 1 sees only the repository-less tag value, which ref_repo() reduces to the
  # tag) nor shape 2 (no `digest` key), so until it was matched explicitly these
  # images were silently never mirrored while the host rewrite still repointed them
  # at the dest registry — a dangling ref that 404s at pull time.
  {
    echo 'image:'
    echo '  repository: iad.ocir.io/idyksih5sir9/cozystack/split'
    printf '  tag: main@sha256:%s\n' "$D"
  } > "$t/system/split/values.yaml"
  # third-party single string -> skipped, and a NUMERIC tag in the same file as a
  # cozystack-owned shape-3 ref. `tag: 123` is ordinary YAML, but yq's test()
  # aborts on a non-string ("cannot match with !!int"), and collect_refs swallows
  # stderr and status — so without a type guard this abort discards the shape-3
  # ref alongside it and `numeric` is silently never mirrored. Co-locating the two
  # in one file is the point: the failure is per-file, not per-value.
  {
    printf 'image: docker.io/clastix/kubectl:1.0@sha256:%s\n' "$D"
    echo 'vendored:'
    echo '  image:'
    echo '    repository: docker.io/vendor/thing'
    echo '    tag: 123'
    echo 'ours:'
    echo '  image:'
    echo '    repository: iad.ocir.io/idyksih5sir9/cozystack/numeric'
    printf '    tag: main@sha256:%s\n' "$D"
  } > "$t/system/third/values.yaml"
  # shapes 2 and 3 with the host split into a sibling `registry` key instead of
  # living inside `repository` — the layout keycloak-operator ships. Rejoining
  # the two is what keeps the ref recognisable: emitting the bare `repository`
  # yields a host-less ref that the SRC_REGISTRY filter discards as third-party,
  # the same silent drop shape 3 exists to fix. Worse here than in promote-retag,
  # because the closing host rewrite matches the literal "$SRC_REGISTRY/" string
  # and a split-out host does not contain it: the ref would be neither copied nor
  # rewritten, publishing a tree that points at the private CI registry.
  {
    echo 'image:'
    echo '  registry: iad.ocir.io'
    echo '  repository: idyksih5sir9/cozystack/splithost'
    printf '  tag: main@sha256:%s\n' "$D"
  } > "$t/system/splithost/values.yaml"
  {
    echo 'image:'
    echo '  registry: iad.ocir.io'
    echo '  repository: idyksih5sir9/cozystack/digesthost'
    echo '  tag: main'
    printf '  digest: sha256:%s\n' "$D"
  } > "$t/system/digesthost/values.yaml"
  # shape 4: the host is a document-level key (global.registry.address) and the
  # image map carries only a bare repository — kube-ovn's wrapper chart, whose
  # own `make image` in cozystack/kubeovn-chart writes exactly this layout. The
  # co-located `other` block is a chart-level image with no digest: it must not
  # be emitted at all, proving the rule filters within global.images rather than
  # blanket-prefixing every entry with the registry address.
  {
    echo 'global:'
    echo '  registry:'
    echo '    address: iad.ocir.io/idyksih5sir9/cozystack'
    echo '  images:'
    echo '    globalreg:'
    echo '      repository: globalreg'
    printf '      tag: v1.2.3@sha256:%s\n' "$D"
    echo '    other:'
    echo '      repository: other'
    echo '      tag: v1.2.3'
  } > "$t/system/globalreg/values.yaml"
  # shape 5: operator (cozystack-owned -> copied) + platformSource (cozystack-packages -> skipped)
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

  # The four cozystack-owned component images are each copied to GHCR by digest.
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/foo@sha256:.* docker://ghcr.io/cozystack/cozystack/foo:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://ghcr.io/cozystack/cozystack/bar:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://ghcr.io/cozystack/cozystack/cozystack-operator:0.0.0-nightly.test' "$tmp/out"
  # shape 3 — the source ref must carry the REPOSITORY, not the bare tag: a rule
  # that matched the tag alone would plan a copy from "main@sha256:..." and be
  # dropped as non-SRC_REGISTRY, so assert the full source ref, not just the dest.
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/split@sha256:.* docker://ghcr.io/cozystack/cozystack/split:0.0.0-nightly.test' "$tmp/out"
  # A shape-3 ref sharing a file with a non-string tag survives — see _make_tree.
  grep -q 'docker://ghcr.io/cozystack/cozystack/numeric:0.0.0-nightly.test' "$tmp/out"
  # Split-out host (`registry` sibling / global.registry.address). Assert the
  # full source ref for the same reason shape 3 does: a rule that dropped the
  # host would plan a copy from a host-less repository, which is filtered out
  # rather than reported, so checking only the destination proves nothing.
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/splithost@sha256:.* docker://ghcr.io/cozystack/cozystack/splithost:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/digesthost@sha256:.* docker://ghcr.io/cozystack/cozystack/digesthost:0.0.0-nightly.test' "$tmp/out"
  grep -q 'docker://iad.ocir.io/idyksih5sir9/cozystack/globalreg@sha256:.* docker://ghcr.io/cozystack/cozystack/globalreg:0.0.0-nightly.test' "$tmp/out"
  # The digest-less sibling under global.images is not invented into a ref.
  ! grep -q '/other' "$tmp/out"
  # A floating tag is moved alongside the pinned version.
  grep -q 'docker://ghcr.io/cozystack/cozystack/foo:nightly' "$tmp/out"

  # Third-party images never appear in the copy plan.
  ! grep -q 'docker.io/clastix' "$tmp/out"
  ! grep -q 'docker.io/vendor' "$tmp/out"
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
