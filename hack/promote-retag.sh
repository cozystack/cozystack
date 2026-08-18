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
# Requires: yq (mikefarah), skopeo, sha256sum, and a registry login already done.
#
# The repo/tag split (ref_repo below) strips the :tag from the last path
# component only, so registry hosts that carry a :port are preserved.
#
# A source tag is not always just the version. Some images are one-per-something
# within a single repository — ubuntu-container-disk is one per Kubernetes minor,
# six digests under one repo tagged v1.30-vX.Y.Z … v1.35-vX.Y.Z — and there the
# part before the version is the only thing telling them apart. Composing the
# destination from the release version alone aimed all six at
# ubuntu-container-disk:vX.Y.Z: the first was written and the second hit the
# write-once guard, mid-finalize, after the stable git tag and the GitHub release
# were already public. ref_tag_prefix below carries that part through, so the
# destination is <prefix><stable> and each source keeps its own tag.
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
[ "$DRY_RUN" -eq 1 ] || command -v sha256sum >/dev/null || { echo "sha256sum is required" >&2; exit 1; }

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

# ref_tag <ref> — the :tag of a "<repo>[:<tag>]@<digest>" ref, or nothing when it
# carries none. Splits on the LAST path component exactly as ref_repo does, so
# the two always agree on where the repo ends and the tag begins.
#
# Only refs collected from an images/*.tag file or a textually scraped extra
# file still carry a tag by the time they reach here; every yq shape in
# hack/lib/image-refs.sh rebuilds the ref as "<repo>@<digest>" and drops it. That
# is fine for the images that need a prefix today (ubuntu-container-disk is a
# .tag file) but it does bound this fix: a prefixed image vendored through one of
# the yq shapes would arrive tagless and get the bare stable tag. Widening the
# library's output is a change to every consumer of it and is not attempted here.
ref_tag() {
  r="${1%@*}"            # strip @digest
  img="${r##*/}"         # last path component (may carry :tag)
  case "$img" in
    *:*) printf '%s' "${img#*:}" ;;
  esac
}

# _is_version <string> — true when <string> is a whole cozystack version: a
# three-component core with an optional prerelease/build suffix. v1.5.4 and
# v1.6.0-rc.1 are versions; v1.30, latest and rc.1 are not.
_is_version() {
  printf '%s\n' "$1" | grep -qE '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z][0-9A-Za-z.-]*)?$'
}

# ref_tag_prefix <tag> — the leading "<something>-" of a source tag that
# qualifies WHICH image in a repository the ref is, as opposed to which version
# of it. Empty when the tag is version-only, or when no split yields a version.
#
# The rule: split at each "-" left to right and stop at the first split whose
# REMAINDER is a whole version; everything consumed so far is the prefix.
# Anchoring on the remainder rather than on the head is what keeps a plain
# prerelease intact — v1.6.0-rc.1 leaves "rc.1", which is not a version, so the
# tag has no prefix and is replaced whole. A last-hyphen split would instead read
# it as "v1.6.0-" + "rc.1" and mis-tag every ref in a real promotion, since an rc
# version string is exactly what a promoted tree's refs carry. Conversely
# v1.30-v1.5.2 leaves "v1.5.2", a version, so "v1.30-" is a prefix.
#
# When nothing matches — "latest", "dev", a git-describe like v1.5.2-3-gabc1234 —
# the prefix is empty and the whole tag is replaced, which is what this script did
# for every ref before prefixes were handled. The fallback is deliberately the
# old behaviour: an unrecognized tag shape cannot start mis-mapping, it can only
# keep doing what it already did.
ref_tag_prefix() {
  _t="$1"
  _p=""
  while :; do
    case "$_t" in
      *-*) ;;
      *) return 0 ;;     # no split left to try; no prefix
    esac
    _p="${_p}${_t%%-*}-"
    _t="${_t#*-}"
    if _is_version "$_t"; then
      printf '%s' "$_p"
      return 0
    fi
  done
}

# manifest_digest <ref> — the digest of <ref>'s raw manifest, or empty output if
# the tag is PROVEN not to exist. Anything else is indeterminate and returns
# non-zero, which under `set -e` aborts the promotion before it writes.
#
# The three states matter because the caller turns "empty" into "not published,
# safe to copy over". Inferring that from a failure — or from a successful
# response with no bytes — is how a rate-limit or a proxy hiccup gets read as
# permission to overwrite released bytes. Absence has to be proven by the
# registry saying so.
manifest_digest() {
  _ref="$1"
  _manifest="$(mktemp)"
  _mderr="$(mktemp)"
  # Hash the raw bytes so this works for images and OCI artifacts alike. Files
  # preserve inspect's status without relying on non-POSIX pipefail.
  if skopeo inspect --raw "docker://$_ref" >"$_manifest" 2>"$_mderr"; then
    if [ -s "$_manifest" ]; then
      printf 'sha256:%s' "$(sha256sum "$_manifest" | cut -d' ' -f1)"
      rm -f "$_manifest" "$_mderr"
      return 0
    fi
    # Exit 0 with no bytes — a registry answering 200 with an empty body. Not a
    # digest (hashing nothing yields sha256:e3b0c442…, which looks real and
    # belongs to no manifest) and not proof of absence either, so refuse to
    # decide rather than let the caller copy over whatever is really there.
    echo "::error::skopeo inspect of ${_ref} succeeded but returned no manifest bytes; refusing to decide whether that tag exists" >&2
    rm -f "$_manifest" "$_mderr"
    return 1
  fi
  # Non-zero. Only a registry that says the manifest is unknown proves absence;
  # 429/5xx/auth/network failures all also exit non-zero with no bytes, and
  # reading those as "not published" is exactly the fail-open this guards.
  if grep -qiE 'manifest unknown|manifest not known|manifest for [^ ]* not found|not found|404' "$_mderr"; then
    rm -f "$_manifest" "$_mderr"
    return 0
  fi
  echo "::error::skopeo inspect of ${_ref} failed and did not report the manifest as unknown, so it cannot be treated as unpublished: $(tr '\n' ' ' <"$_mderr")" >&2
  rm -f "$_manifest" "$_mderr"
  return 1
}

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

# Normalize every collected ref to the canonical "<repo>@<digest>|<prefix>" form
# so the dedup is on the real identity — the same image vendored in two shapes is
# retagged once, not twice. The cosmetic version part of the source :tag is
# dropped; only the prefix that identifies which image in the repo this is
# survives, because that is the part the destination tag has to reproduce.
#
# "|" is the field separator because no registry host, repository path, tag or
# digest may contain one, so both halves are recoverable with a plain parameter
# expansion, and an absent prefix is an unambiguous empty trailing field rather
# than a missing one.
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
  refs="${refs}${_repo}@$(ref_digest "$raw")|$(ref_tag_prefix "$(ref_tag "$raw")")
"
done
refs="$(printf '%s' "$refs" | sort -u)"
[ -n "$refs" ] || { echo "No cozystack-owned digest-pinned image refs found under ${REGISTRY}/ — is this the rc's baked tree?" >&2; exit 1; }

echo "$refs" | while IFS= read -r record; do
  [ -n "$record" ] || continue
  ref="${record%|*}"
  prefix="${record##*|}"
  repo="${ref%@*}"
  digest="${ref##*@}"
  # <prefix><stable> is the destination tag throughout: the stable tag, the
  # write-once probe, the post-copy verify and :latest must all name the same one.
  stable_tag="${prefix}${STABLE}"
  echo "▸ ${repo}:${stable_tag}  ${digest}"
  # The stable tag is write-once at the image level: resolve the destination's
  # raw manifest digest before copying. No-op if it already points at this rc
  # digest (idempotent re-run), fail if it points elsewhere (a partial run or
  # manual push already put different bytes there) rather than mutate released
  # bytes. :latest is intentionally mutable and (re)pointed below only when
  # MOVE_LATEST=1.
  if [ "$DRY_RUN" -eq 0 ]; then
    cur="$(manifest_digest "${repo}:${stable_tag}")"
    if [ -n "$cur" ] && [ "$cur" != "$digest" ]; then
      echo "::error::${repo}:${stable_tag} already exists at '${cur}'; refusing to move it to '${digest}' (stable image tags are write-once)" >&2
      exit 1
    fi
    if [ "$cur" = "$digest" ]; then
      echo "  = ${repo}:${stable_tag} already at ${digest}; skipping stable copy"
    else
      copy "$ref" "${repo}:${stable_tag}"
    fi
  else
    copy "$ref" "${repo}:${stable_tag}"
  fi
  # The floating tag is prefixed for the same reason the stable one is: without
  # it, every per-Kubernetes-minor disk copies to <repo>:latest and the tag ends
  # up on whichever digest the sort put last — a floating tag that names one
  # minor's image while reading as if it named the repository's.
  [ "$MOVE_LATEST" = "1" ] && copy "$ref" "${repo}:${prefix}latest"
  # Verify the stable tag now resolves to the exact rc digest (skip in dry-run).
  if [ "$DRY_RUN" -eq 0 ]; then
    got="$(manifest_digest "${repo}:${stable_tag}")"
    if [ "$got" != "$digest" ]; then
      echo "::error::${repo}:${stable_tag} resolved to '${got}', expected '${digest}'" >&2
      exit 1
    fi
  fi
done

if [ "$MOVE_LATEST" = "1" ]; then
  echo "Retagged image refs to ${STABLE} (+latest)."
else
  echo "Retagged image refs to ${STABLE} (:latest left unmoved; MOVE_LATEST=0)."
fi
