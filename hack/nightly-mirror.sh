#!/bin/sh
# Mirror the latest main-branch build's cozystack-owned component images from the
# CI build registry (OCIR) to the public release registry (GHCR) BY DIGEST — no
# rebuild — and rewrite the baked package tree so the GHCR install closure is
# self-contained on GHCR.
#
# A nightly is a *renamed copy* of what main already built: every image cozystack
# ships is content-addressed, so copying each `<repo>@<digest>` from the source
# registry to the destination registry preserves the digest bit-for-bit. The
# nightly that users install from GHCR is the exact image set built and cached on
# the last push to main — nothing is rebuilt here.
#
# Usage: hack/nightly-mirror.sh <version> <baked-tree-dir> [--dry-run]
#   <version>         dest tag, e.g. 0.0.0-nightly.20260626 (a floating tag is
#                     also moved — see FLOATING)
#   <baked-tree-dir>  the extracted cozystack-packages OCI artifact: a directory
#                     whose layout is <dir>/<group>/<pkg>/values.yaml (apps/,
#                     core/, system/, extra/). This is the digest-vendored tree
#                     main's build baked but never committed.
#   --dry-run         print the skopeo copies and the host rewrite without doing
#                     them
#
# On success the baked tree's values.yaml files have every SRC_REGISTRY image
# host rewritten to DST_REGISTRY (digests untouched). The caller (nightly.yaml)
# then re-pushes that rewritten tree as the GHCR cozystack-packages artifact and
# repackages the cozy-installer chart against it — this script deliberately does
# NOT touch the cozystack-packages artifact itself (it is rebuilt downstream
# from the rewritten content, so a copy here would just be overwritten).
#
# Requires: yq (mikefarah), skopeo, and a login to both registries already done.
set -eu

VERSION="${1:?usage: nightly-mirror.sh <version> <baked-tree-dir> [--dry-run]}"
TREE="${2:?usage: nightly-mirror.sh <version> <baked-tree-dir> [--dry-run]}"
DRY_RUN=0
[ "${3:-}" = "--dry-run" ] && DRY_RUN=1

# Source = the per-CI build registry main pushes to; dest = the public release
# registry nightlies are served from. Defaults mirror hack/common-envs.mk
# (REGISTRY) and the CI workflows; override either for a fork.
SRC_REGISTRY="${SRC_REGISTRY:-iad.ocir.io/idyksih5sir9/cozystack}"
DST_REGISTRY="${DST_REGISTRY:-ghcr.io/cozystack/cozystack}"
# Floating tag moved to this nightly in addition to the pinned <version>.
FLOATING="${FLOATING:-nightly}"

[ -d "$TREE" ] || { echo "baked-tree-dir '$TREE' is not a directory" >&2; exit 1; }
command -v yq >/dev/null     || { echo "yq (mikefarah) is required" >&2; exit 1; }
yq --version 2>&1 | grep -q mikefarah || { echo "yq (mikefarah) is required" >&2; exit 1; }
[ "$DRY_RUN" -eq 1 ] || command -v skopeo >/dev/null || { echo "skopeo is required" >&2; exit 1; }

# Collect "repo@sha256:..." refs from every package values.yaml, across the three
# shapes the build writes (identical to hack/promote-retag.sh):
#   1. single string  <repo>:<tag>@sha256:<digest>
#   2. split map       {repository, tag, digest}
#   3. OCI artifact    {platformSourceUrl: oci://<repo>, platformSourceRef: digest=...}
collect_refs() {
  for f in "$TREE"/*/*/values.yaml; do
    [ -f "$f" ] || continue
    yq -r '.. | select(tag == "!!str") | select(test("@sha256:[0-9a-f]{64}"))' "$f" 2>/dev/null || true
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("digest")) | .repository + "@" + .digest' "$f" 2>/dev/null || true
    yq -r '.. | select(tag == "!!map") | select(has("platformSourceUrl") and has("platformSourceRef")) | (.platformSourceUrl | sub("^oci://"; "")) + "@" + (.platformSourceRef | sub("^digest="; ""))' "$f" 2>/dev/null || true
  done
}

# Split a "<repo>[:<tag>]@sha256:<digest>" ref into repo and digest, stripping
# the :tag from the LAST path component only so a host :port is preserved.
ref_repo() {
  r="${1%@*}"
  img="${r##*/}"
  if [ "$r" = "$img" ]; then
    printf '%s' "${img%:*}"
  else
    printf '%s/%s' "${r%/*}" "${img%:*}"
  fi
}
ref_digest() { printf '%s' "${1##*@}"; }

copy() {
  _src="$1"; _dst="$2"
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY-RUN skopeo copy --multi-arch all docker://$_src docker://$_dst"
    return 0
  fi
  skopeo copy --multi-arch all "docker://$_src" "docker://$_dst"
}

# Normalize to canonical "<repo>@<digest>", keep only SRC_REGISTRY-owned refs
# (third-party images live in registries this job cannot push to), and drop the
# cozystack-packages artifact — the caller rebuilds it from the rewritten tree,
# so copying it here would only be overwritten downstream. Dedup on real identity.
refs=""
for raw in $(collect_refs); do
  [ -n "$raw" ] || continue
  _repo="$(ref_repo "$raw")"
  case "$_repo" in
    "${SRC_REGISTRY}/cozystack-packages") continue ;;
    "${SRC_REGISTRY}/"*) ;;
    *) continue ;;
  esac
  refs="${refs}${_repo}@$(ref_digest "$raw")
"
done
refs="$(printf '%s' "$refs" | sort -u)"
[ -n "$refs" ] || { echo "No cozystack-owned digest-pinned image refs found under ${SRC_REGISTRY}/ in '${TREE}' — is this the baked main tree?" >&2; exit 1; }

echo "$refs" | while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  src_repo="${ref%@*}"
  digest="${ref##*@}"
  # Swap the source host prefix for the dest host; the path tail is identical.
  dst_repo="${DST_REGISTRY}/${src_repo#"${SRC_REGISTRY}/"}"
  echo "▸ ${src_repo}  ->  ${dst_repo}  ${digest}"
  copy "${src_repo}@${digest}" "${dst_repo}:${VERSION}"
  copy "${src_repo}@${digest}" "${dst_repo}:${FLOATING}"
  # Verify the dest pinned tag resolves to the exact source digest.
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(skopeo inspect --format '{{.Digest}}' "docker://${dst_repo}:${VERSION}" 2>/dev/null || echo '')"
    if [ "$got" != "$digest" ]; then
      echo "::error::${dst_repo}:${VERSION} resolved to '${got}', expected '${digest}'" >&2
      exit 1
    fi
  fi
done

# Rewrite the image host SRC_REGISTRY -> DST_REGISTRY across the whole baked tree,
# digests untouched. Only cozystack-owned refs carry the SRC_REGISTRY prefix, so a
# literal substring replace cannot touch third-party image hosts. Escape the dots
# so sed matches the literal host, not "any character". This also rewrites the
# platformSourceUrl (oci://SRC/cozystack-packages -> oci://DST/cozystack-packages);
# its digest (platformSourceRef) is reset by the caller after the GHCR re-push.
SRC_ESC=$(printf '%s' "$SRC_REGISTRY" | sed -e 's/[].[^$*/\\]/\\&/g')
if [ "$DRY_RUN" -eq 1 ]; then
  echo "DRY-RUN sed -i 's|${SRC_REGISTRY}/|${DST_REGISTRY}/|g' over ${TREE}/*/*/values.yaml"
else
  # Same depth-2 glob as collect_refs: the files whose hosts are rewritten are
  # exactly the files scanned for images to mirror. A `find -name values.yaml`
  # (any depth) could host-rewrite a deeper values.yaml whose image was never
  # mirrored, leaving a dangling GHCR ref.
  for f in "$TREE"/*/*/values.yaml; do
    [ -f "$f" ] || continue
    sed -i "s|${SRC_ESC}/|${DST_REGISTRY}/|g" "$f"
  done
fi

echo "Mirrored cozystack-owned images to ${DST_REGISTRY} (:${VERSION} +:${FLOATING}) and rewrote tree hosts."
