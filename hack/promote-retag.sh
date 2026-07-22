#!/bin/sh
# Promote a release-candidate to stable by RETAGGING its already-built images —
# no rebuild. Every cluster artifact cozystack ships (component images, the
# cozystack-packages OCI artifact) is content-addressed; this script reads the
# digests the rc baked into the package values.yaml files and copies each, by
# digest, to the stable tag. Because the copy source is the immutable digest,
# the stable image is bit-for-bit the rc image that passed e2e — promotion
# cannot diverge from what was tested.
#
# Usage: hack/promote-retag.sh <stable-version> [--dry-run]
#   <stable-version>  e.g. v1.4.0  (the tag the rc is promoted to)
#   --dry-run         print the skopeo copies without executing them
#
# Environment:
#   MOVE_LATEST=1     also (re)point :latest at each promoted image. OFF by
#                     default: :latest must move only when the version being
#                     promoted is the newest published stable, otherwise a patch
#                     on an older line (e.g. v1.4.6 while 1.5.x is current) would
#                     drag :latest backwards. The caller (finalize) computes this
#                     with the same max-semver test it uses for the release's
#                     make_latest flag and sets MOVE_LATEST accordingly.
#
# Reads image refs from the CURRENT tree (see hack/lib/image-refs.sh for which
# files that covers), which is expected to be the promoted stable
# digest-vendored tree (the release-X.Y.Z branch, whose digests are the rc's —
# only the cosmetic tag string differs).
# Requires: yq (mikefarah), skopeo, and a registry login already done.
#
# The repo/tag split (ref_repo below) strips the :tag from the last path
# component only, so registry hosts that carry a :port are preserved.
set -eu

STABLE="${1:?usage: promote-retag.sh <stable-version> [--dry-run]}"
DRY_RUN=0
[ "${2:-}" = "--dry-run" ] && DRY_RUN=1
# :latest is repointed only when the caller asserts this is the newest stable.
MOVE_LATEST="${MOVE_LATEST:-0}"

# Only cozystack-owned images (those the build pushed to $REGISTRY) are
# retagged. Everything else vendored by digest — third-party images and bare
# upstream tags — lives in registries this job cannot push to. Override
# REGISTRY to match a fork's build registry; the default mirrors
# hack/common-envs.mk.
REGISTRY="${REGISTRY:-ghcr.io/cozystack/cozystack}"

command -v yq >/dev/null     || { echo "yq (mikefarah) is required" >&2; exit 1; }
# The queries below use mikefarah syntax; reject python-yq and other variants
# (mirrors the build-deps check in the Makefile).
yq --version 2>&1 | grep -q mikefarah || { echo "yq (mikefarah) is required" >&2; exit 1; }
# skopeo is only needed to actually copy; a --dry-run just prints the plan.
[ "$DRY_RUN" -eq 1 ] || command -v skopeo >/dev/null || { echo "skopeo is required" >&2; exit 1; }

# Ref collection (which files are scanned, and the YAML shapes within them) is
# shared with hack/nightly-mirror.sh and hack/promote-rewrite-tags.sh — see
# hack/lib/image-refs.sh. It was duplicated between the first two for as long
# as both existed, and they drifted: this script and the mirror scanned only
# the depth-2 values.yaml, so every ref stored in an images/*.tag file or
# stamped into a template was silently skipped. That is the same failure mode
# the shape-3 rule was added for — the promotion reports success while never
# creating those images' :<version> tags. Because the retag happens within one
# registry the digests still resolve and digest-pinned installs are unaffected,
# but the release does not carry the tags it claims to. Keep the enumeration in
# one place so a fix reaches every consumer at once.
# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

collect_refs() { collect_image_refs packages; }

# Split a "<repo>[:<tag>]@sha256:<digest>" ref into repo and digest.
# ref_repo strips the :tag from the LAST path component only, so a registry
# host that carries a :port (e.g. localhost:5000/cozystack/operator, with no
# tag) keeps its port instead of being truncated to the host.
ref_repo() {
  r="${1%@*}"            # strip @digest
  img="${r##*/}"         # last path component (may carry :tag)
  if [ "$r" = "$img" ]; then
    printf '%s' "${img%:*}"
  else
    printf '%s/%s' "${r%/*}" "${img%:*}"
  fi
}
ref_digest() { printf '%s' "${1##*@}"; }               # sha256:...

copy() {
  _src="$1"; _dst="$2"
  if [ "$DRY_RUN" -eq 1 ]; then
    echo "DRY-RUN skopeo copy --multi-arch all docker://$_src docker://$_dst"
    return 0
  fi
  # --multi-arch all copies the whole manifest list (every platform); it is
  # mutually exclusive with the deprecated --all alias, do not combine them.
  skopeo copy --multi-arch all "docker://$_src" "docker://$_dst"
}

# Normalize every collected ref to the canonical "<repo>@<digest>" form (drops
# the cosmetic :tag from shape 1) so the dedup is on the real identity — the
# same image vendored in two shapes is retagged once, not twice.
refs=""
for raw in $(collect_refs); do
  [ -n "$raw" ] || continue
  _repo="$(ref_repo "$raw")"
  # Retag only cozystack-owned images: those the build pushes to $REGISTRY, so
  # a skopeo copy can succeed. Everything else is vendored by digest from a
  # registry this job cannot push to, and a copy there would fail and (under
  # set -e) abort the whole promotion. What this drops today:
  #   - third-party hosts: docker.io/clastix/kubectl, ghcr.io/kvaps/...,
  #     ghcr.io/lexfrei/{kuberture,ouroboros} (deliberately not mirrored under
  #     ghcr.io/cozystack — see those packages' values.yaml)
  #   - ghcr.io/cozystack/ingress-nginx-with-protobuf-exporter/*, which is a
  #     cozystack-org repo but sits outside $REGISTRY's path
  #   - non-ref scalars, e.g. a "--migrate-image=..." arg string
  # Note kilo, kube-ovn and keycloak-operator are NOT in that list: all three
  # are built and pushed to $REGISTRY, and are selected by shapes 3 and 4. An
  # earlier version of this comment named them as unpushed third parties, which
  # is what let their missing release tags go unnoticed.
  case "$_repo" in
    "${REGISTRY}/"*) ;;
    *) continue ;;
  esac
  refs="${refs}${_repo}@$(ref_digest "$raw")
"
done
refs="$(printf '%s' "$refs" | sort -u)"
[ -n "$refs" ] || { echo "No cozystack-owned digest-pinned image refs found under ${REGISTRY}/ — is this the rc's baked tree?" >&2; exit 1; }

echo "$refs" | while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  repo="${ref%@*}"
  digest="${ref##*@}"
  echo "▸ ${repo}  ${digest}"
  # The stable tag is write-once at the image level: inspect the destination
  # before copying. No-op if it already points at this rc digest (idempotent
  # re-run), fail if it points elsewhere (a partial run or manual push already
  # put different bytes there) rather than mutate released bytes. :latest is
  # intentionally mutable and (re)pointed below only when MOVE_LATEST=1.
  if [ "$DRY_RUN" -eq 0 ]; then
    cur="$(skopeo inspect --format '{{.Digest}}' "docker://${repo}:${STABLE}" 2>/dev/null || echo '')"
    if [ -n "$cur" ] && [ "$cur" != "$digest" ]; then
      echo "::error::${repo}:${STABLE} already exists at '${cur}'; refusing to move it to '${digest}' (stable image tags are write-once)" >&2
      exit 1
    fi
    if [ "$cur" = "$digest" ]; then
      echo "  = ${repo}:${STABLE} already at ${digest}; skipping stable copy"
    else
      copy "$ref" "${repo}:${STABLE}"
    fi
  else
    copy "$ref" "${repo}:${STABLE}"
  fi
  [ "$MOVE_LATEST" = "1" ] && copy "$ref" "${repo}:latest"
  # Verify the stable tag now resolves to the exact rc digest (skip in dry-run).
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(skopeo inspect --format '{{.Digest}}' "docker://${repo}:${STABLE}" 2>/dev/null || echo '')"
    if [ "$got" != "$digest" ]; then
      echo "::error::${repo}:${STABLE} resolved to '${got}', expected '${digest}'" >&2
      exit 1
    fi
  fi
done

if [ "$MOVE_LATEST" = "1" ]; then
  echo "Retagged image refs to ${STABLE} (+latest)."
else
  echo "Retagged image refs to ${STABLE} (:latest left unmoved; MOVE_LATEST=0)."
fi
