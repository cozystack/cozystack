#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The site-router boot disk is the only place in this tree where a stamped image
# reference is fed to CDI instead of to a kubelet, and CDI is stricter: its
# docker:// transport cannot parse a reference that carries BOTH a tag and a
# digest, and rejects it outright.
#
#     Could not parse image: Docker references with both a tag and digest
#     are currently not supported
#
# The committed stamp is `<repo>:<tag>@sha256:<digest>` — the shape
# hack/lib/image-refs.sh greps for, which the promote/retag/mirror tooling reads
# from there — so the chart drops the tag at the point of consumption
# (site-router.applianceDiskUrl). Nothing else in the repo does this, which is
# why nothing else caught it: the same reference pulls fine through kubelet, and
# the failure surfaces only as a DataVolume in ImportInProgress /
# DataVolumeError with an importer restarting forever.
#
# The defect class this suite pins is "a reference shape that renders fine and
# then fails at import". That needs a renderer fed a *stamped* reference, which
# helm-unittest cannot do — it has no way to inject chart files, and the
# committed images/vyos-router-disk.tag is the digest-less v0.0.0 placeholder
# (deliberately, see packages/apps/site-router/docs/image-lifecycle.md). So the
# rendered-URL assertions live here, where the .tag can be written, and
# packages/apps/site-router/tests/dv_test.yaml asserts the guard that rejects
# the placeholder. Once a release build stamps a real digest into the .tag, the
# shape assertions below can move back into that suite.
#
# Harness note: the CI path is hack/cozytest.sh, NOT real bats. There is no
# `run`, `$status`, `$output`, `skip`, or setup()/teardown(); each test runs as a
# shell function under `set -eu -x`, so a non-zero exit is the failure, and TMP
# is provisioned per test. Compatible with `bats` directly as well.
#
# Run with: hack/cozytest.sh hack/site-router-appliance-ref.bats
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
CHART_SRC="$REPO_ROOT/packages/apps/site-router"

# Fixture reference. The registry is a documentation host (RFC 2606 .example) and
# the digest is synthetic: no real registry, cluster or build appears here. It is
# also deliberately not the all-zero digest, which hack/image-refs-no-placeholder
# .bats rejects tree-wide as a pin that resolves nowhere.
FIX_REPO="registry.example/site-router/vyos-router-disk"
FIX_DIGEST="sha256:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

# Render the chart's boot DataVolume with $1 stamped into the .tag file.
#
# Results land in files, not in this function's exit status: cozytest.sh rewrites
# a closing brace on its own line to `return 0`, so a helper cannot report
# failure through its status. stdout -> $TMP/rendered.yaml, stderr -> $TMP/err,
# helm's exit code -> $TMP/rc, all asserted by the caller.
#
# The chart is copied rather than edited in place so a failed run cannot leave a
# fixture reference in the working tree. `cp -RL` dereferences charts/cozy-lib,
# which is a symlink to packages/library and would dangle once copied.
render_dv() {
  rm -rf "$TMP/chart"
  cp -RL "$CHART_SRC" "$TMP/chart"
  printf '%s\n' "$1" > "$TMP/chart/images/vyos-router-disk.tag"
  rc=0
  helm template site-router-test "$TMP/chart" -n tenant-test -s templates/dv.yaml \
    > "$TMP/rendered.yaml" 2> "$TMP/err" || rc=$?
  printf '%s\n' "$rc" > "$TMP/rc"
}

@test "a stamped tag+digest reference imports by digest, with the tag dropped" {
  TMP=$(mktemp -d)
  render_dv "$FIX_REPO:v1.6.0@$FIX_DIGEST"
  [ "$(cat "$TMP/rc")" = 0 ] || { cat "$TMP/err" >&2; rm -rf "$TMP"; exit 1; }
  grep -qF "url: \"docker://$FIX_REPO@$FIX_DIGEST\"" "$TMP/rendered.yaml"
  # The CDI-fatal shape, stated as its own assertion: a URL keeping both the tag
  # and the digest is what the importer refuses to parse.
  ! grep -q 'url: .*:v1\.6\.0@sha256:' "$TMP/rendered.yaml"
  rm -rf "$TMP"
}

@test "a digest-only reference passes through unchanged" {
  TMP=$(mktemp -d)
  render_dv "$FIX_REPO@$FIX_DIGEST"
  [ "$(cat "$TMP/rc")" = 0 ] || { cat "$TMP/err" >&2; rm -rf "$TMP"; exit 1; }
  grep -qF "url: \"docker://$FIX_REPO@$FIX_DIGEST\"" "$TMP/rendered.yaml"
  rm -rf "$TMP"
}

@test "a reference carrying no digest fails the render, naming the file and the remedy" {
  TMP=$(mktemp -d)
  render_dv "$FIX_REPO:v0.0.0"
  [ "$(cat "$TMP/rc")" != 0 ]
  [ ! -s "$TMP/rendered.yaml" ]
  grep -q 'images/vyos-router-disk.tag' "$TMP/err"
  grep -q 'carries no @sha256: digest' "$TMP/err"
  grep -q 'packages/system/vyos-router-image image' "$TMP/err"
  rm -rf "$TMP"
}

@test "a malformed digest fails the render instead of reaching the importer" {
  TMP=$(mktemp -d)
  render_dv "$FIX_REPO:v1.6.0@sha256:deadbeef"
  [ "$(cat "$TMP/rc")" != 0 ]
  [ ! -s "$TMP/rendered.yaml" ]
  grep -q 'not a digest-pinned image reference' "$TMP/err"
  rm -rf "$TMP"
}

# The rest of the DataVolume's shape, asserted against a stamped reference
# because that is the only state in which the chart renders. R1: the request is
# 12Gi, not the 10Gi virtual size of the VyOS qcow2 — CDI's filesystem-import
# scratch (decompressed image + ~5.5% fs overhead) overflows a request equal to
# the virtual size and fails the import live on VyOS 1.5-rolling. storageClass is
# pinned to `replicated` (DRBD/LINSTOR) so the boot disk stays live-migratable.
@test "the boot DataVolume keeps its live-validated shape" {
  TMP=$(mktemp -d)
  render_dv "$FIX_REPO:v1.6.0@$FIX_DIGEST"
  [ "$(cat "$TMP/rc")" = 0 ] || { cat "$TMP/err" >&2; rm -rf "$TMP"; exit 1; }
  grep -q '^kind: DataVolume$' "$TMP/rendered.yaml"
  grep -q '^  name: site-router-test-boot$' "$TMP/rendered.yaml"
  grep -q '^  contentType: kubevirt$' "$TMP/rendered.yaml"
  grep -q '^    storageClassName: replicated$' "$TMP/rendered.yaml"
  grep -q '^        storage: 12Gi$' "$TMP/rendered.yaml"
  # The source is the registry importer alone: not http (cannot verify a sha256)
  # and not a clone of a shared golden (populated only at creation, so advancing
  # the appliance could never replace it).
  ! grep -qE '^ +(http|pvc):' "$TMP/rendered.yaml"
  rm -rf "$TMP"
}
