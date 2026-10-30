#!/bin/bash
# Sign every first-party image a release pins, keylessly, and verify the result.
#
# Usage: hack/sign-release-images.sh [--verify-only] [--list <file>] <packages-root> <registry>
#   <packages-root>  a tree hack/stitch-multiarch.sh has repinned (packages/, or
#                    an extracted artifact with the same <group>/<pkg> layout)
#   <registry>       the registry the release pushed to; a ref under it is
#                    first-party, any other is vendored and left alone
#   --verify-only    sign nothing: fail unless every ref already verifies,
#                    which is how promote-rc.yaml checks an rc before staging
#                    the stable release on its digests
#   --list <file>    write the refs, one <repo>@<digest> per line, sorted: the
#                    cozystack-images.txt release asset
#
# Every <registry>/<repo>[:<tag>]@<digest> that hack/lib/image-refs.sh finds in
# the tree is signed by its digest with cosign and the job's GitHub OIDC
# identity, bar the packages artifact, which promotion republishes under a new
# digest. After the stitch a digest names a multi-arch index, and a signature
# over an index covers every manifest it lists by digest, the attestation
# manifest an image built with SBOM=1 carries included, so the SBOM needs no
# signature of its own. cosign v3 stores the signature as a Sigstore bundle,
# an OCI referrer of the digest in the same repository; GHCR has no referrers
# API, so it lands in the fallback tag sha256-<hex> that cosign verify reads
# back. hack/promote-retag.sh copies a digest to its stable tag inside that
# repository, so the stable release carries the signatures made for its rc.
#
# A ref that already verifies is not signed again: a re-run of prepare-release
# repins the same digests, and a second signature would say nothing the first
# does not. The rest are signed in one cosign call, which still makes a key, a
# Fulcio certificate and a Rekor entry of its own for every ref. Each
# ref is then verified against the identity docs/release.md tells users to
# check, so a signature that check would reject fails the job before
# hack/upload-assets.sh runs and nothing is uploaded.
#
# Needs cosign on PATH, yq (mikefarah) to read the tree, a registry login
# cosign reads (DOCKER_CONFIG) and, unless --verify-only, `id-token: write` in
# the job.
set -euo pipefail

# The workflows that publish releases, at the refs they run from. Images are
# signed by tags.yaml only, on a v* tag push, so a verified image certificate
# names that run; the promote-rc.yaml alternative is kept so that the one
# expression in docs/release.md checks every release signature.
# hack/sign-release-images.bats holds it equal to the one in
# hack/sign-release-assets.sh.
IDENTITY_REGEXP='^https://github\.com/cozystack/cozystack/\.github/workflows/(tags\.yaml@refs/tags/v|promote-rc\.yaml@refs/heads/main$)'
OIDC_ISSUER=https://token.actions.githubusercontent.com

usage() {
  echo "usage: $0 [--verify-only] [--list <file>] <packages-root> <registry>" >&2
  exit 2
}

verify_only=0
list=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --verify-only) verify_only=1; shift ;;
    --list) [ "$#" -ge 2 ] || usage; list="$2"; shift 2 ;;
    -*) usage ;;
    *) break ;;
  esac
done
[ "$#" -eq 2 ] || usage
ROOT="${1%/}"
REGISTRY="${2%/}"
[ -d "$ROOT" ] || { echo "packages-root '$ROOT' is not a directory" >&2; exit 2; }
for tool in cosign yq; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# One "<repo>@<digest>" per first-party image. grep -o drops anything around a
# ref, such as kamaji's --migrate-image= prefix, and the sed drops the tag: a
# registry client rejects a ref carrying both, and the digest is what is signed.
# The packages artifact is an OCI artifact promotion republishes, not an image.
reg_re="$(printf '%s' "$REGISTRY" | sed -e 's/[].[^$*/\\]/\\&/g')"
refs="$(collect_image_refs "$ROOT" \
  | grep -oE "${reg_re}/[^@[:space:]\"']+@sha256:[0-9a-f]{64}" \
  | sed -E 's/:[^/@]+@/@/' \
  | grep -v "^${reg_re}/cozystack-packages@" \
  | sort -u)" || true
[ -n "$refs" ] || { echo "no ${REGISTRY}/ refs found under ${ROOT}; is this a tree the build stamped?" >&2; exit 1; }

verified() {
  cosign verify \
    --certificate-identity-regexp "$IDENTITY_REGEXP" \
    --certificate-oidc-issuer "$OIDC_ISSUER" \
    "$1" >/dev/null
}

unsigned=()
already=0
# fd 3, so a registry client that reads stdin cannot swallow the list.
while IFS= read -r ref <&3; do
  if verified "$ref" 2>/dev/null; then
    echo "  = ${ref} already signed"
    already=$((already + 1))
  else
    unsigned+=("$ref")
  fi
done 3<<<"$refs"

if [ "$verify_only" -eq 1 ]; then
  for ref in ${unsigned[@]+"${unsigned[@]}"}; do
    echo "::error::${ref} carries no signature this release's identity verifies" >&2
  done
  [ "${#unsigned[@]}" -eq 0 ] || exit 1
else
  if [ "${#unsigned[@]}" -gt 0 ]; then
    cosign sign --yes "${unsigned[@]}"
  fi
  for ref in ${unsigned[@]+"${unsigned[@]}"}; do
    verified "$ref" || { echo "::error::${ref}: the signature just made does not verify against ${IDENTITY_REGEXP}" >&2; exit 1; }
  done
fi

echo "sign-images: ${#unsigned[@]} signed, ${already} already signed"
if [ -n "$list" ]; then
  printf '%s\n' "$refs" >"$list"
fi
