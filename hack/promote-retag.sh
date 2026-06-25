#!/bin/sh
# Promote a release-candidate to stable by RETAGGING its already-built images —
# no rebuild. Every cluster artifact cozystack ships (component images, the
# cozystack-packages OCI artifact) is content-addressed; this script reads the
# digests the rc baked into the package values.yaml files and copies each, by
# digest, to the stable tag (and :latest). Because the copy source is the
# immutable digest, the stable image is bit-for-bit the rc image that passed
# e2e — promotion cannot diverge from what was tested.
#
# Usage: hack/promote-retag.sh <stable-version> [--dry-run]
#   <stable-version>  e.g. v1.4.0  (the tag the rc is promoted to; :latest too)
#   --dry-run         print the skopeo copies without executing them
#
# Reads image refs from packages/*/*/values.yaml in the CURRENT tree, which is
# expected to be the rc's digest-vendored tree (the release-X.Y.Z-rc.N staging
# branch). Requires: yq (mikefarah), skopeo, and a registry login already done.
#
# The repo/tag split (ref_repo below) strips the :tag from the last path
# component only, so registry hosts that carry a :port are preserved.
set -eu

STABLE="${1:?usage: promote-retag.sh <stable-version> [--dry-run]}"
DRY_RUN=0
[ "${2:-}" = "--dry-run" ] && DRY_RUN=1

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

# Collect "repo@sha256:..." refs from every package values.yaml, across the
# three shapes the build writes:
#   1. single string  <repo>:<tag>@sha256:<digest>   (e.g. .cozystackAPI.image)
#   2. split map       {repository, tag, digest}      (e.g. .cilium.image)
#   3. OCI artifact    {platformSourceUrl: oci://<repo>, platformSourceRef: digest=sha256:<digest>}
collect_refs() {
  for f in packages/*/*/values.yaml; do
    [ -f "$f" ] || continue
    # shape 1
    yq -r '.. | select(tag == "!!str") | select(test("@sha256:[0-9a-f]{64}"))' "$f" 2>/dev/null || true
    # shape 2
    yq -r '.. | select(tag == "!!map") | select(has("repository") and has("digest")) | .repository + "@" + .digest' "$f" 2>/dev/null || true
    # shape 3
    yq -r '.. | select(tag == "!!map") | select(has("platformSourceUrl") and has("platformSourceRef")) | (.platformSourceUrl | sub("^oci://"; "")) + "@" + (.platformSourceRef | sub("^digest="; ""))' "$f" 2>/dev/null || true
  done
}

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
  # Retag only cozystack-owned images. This drops third-party images
  # (docker.io/clastix/kubectl, ghcr.io/kvaps/...), bare upstream tags
  # (kube-ovn/keycloak/kilo) and non-ref scalars (e.g. a "--migrate-image=..."
  # arg string) — all vendored by digest but not pushed to $REGISTRY, so a
  # skopeo copy to them would fail and (under set -e) abort the whole promotion.
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
  copy "$ref" "${repo}:${STABLE}"
  copy "$ref" "${repo}:latest"
  # Verify the stable tag now resolves to the exact rc digest (skip in dry-run).
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(skopeo inspect --format '{{.Digest}}' "docker://${repo}:${STABLE}" 2>/dev/null || echo '')"
    if [ "$got" != "$digest" ]; then
      echo "::error::${repo}:${STABLE} resolved to '${got}', expected '${digest}'" >&2
      exit 1
    fi
  fi
done

echo "Retagged image refs to ${STABLE} (+latest)."
