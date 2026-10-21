#!/bin/bash
# Gather the SBOM of every first-party image a release pins into one archive.
#
# Usage: hack/collect-image-sboms.sh <packages-root> <registry> <archive.tar.gz>
#   <packages-root>  a tree hack/stitch-multiarch.sh has repinned (packages/, or
#                    an extracted artifact with the same <group>/<pkg> layout)
#   <registry>       the registry the release pushed to; a ref under it is
#                    first-party, any other is vendored and left alone
#   <archive.tar.gz> the cozystack-images-sbom.tar.gz release asset to write
#
# BuildKit attaches an SPDX SBOM to each image as an attestation manifest of
# its index (SBOM=1 in hack/common-envs.mk), one document per platform, and
# `docker buildx imagetools inspect` reads it back from the registry, and with
# --raw the platforms the index serves. The archive holds
# <image>.<os>-<arch>.spdx.json per platform of each image, or <image>.spdx.json
# for an image with one platform, whose SBOM carries no platform key, so a user
# gets every document from one signed download rather than one registry read
# per image. The packages artifact is not an image and is left out.
#
# An image without an SBOM for every platform its index serves fails the run,
# naming it and the platforms: every first-party image of a release is built
# with SBOM=1, so a missing one is a build that lost its attestation, not an
# image that cannot carry one.
#
# The archive is written with a fixed member order, time and owner and without
# a gzip timestamp, so a re-run on the same tree writes the same bytes.
#
# Requires docker with buildx, jq, yq (mikefarah), GNU tar and gzip, and a
# registry login docker reads (DOCKER_CONFIG).
set -euo pipefail

usage() {
  echo "usage: $0 <packages-root> <registry> <archive.tar.gz>" >&2
  exit 2
}

[ "$#" -eq 3 ] || usage
ROOT="${1%/}"
REGISTRY="${2%/}"
ARCHIVE="$3"
[ -d "$ROOT" ] || { echo "packages-root '$ROOT' is not a directory" >&2; exit 2; }
for tool in docker jq yq tar gzip; do
  command -v "$tool" >/dev/null || { echo "$tool is required" >&2; exit 2; }
done

# shellcheck source=hack/lib/image-refs.sh
. "$(dirname "$0")/lib/image-refs.sh"

# One "<repo>@<digest>" per first-party image, as in hack/sign-release-images.sh.
reg_re="$(printf '%s' "$REGISTRY" | sed -e 's/[].[^$*/\\]/\\&/g')"
refs="$(collect_image_refs "$ROOT" \
  | grep -oE "${reg_re}/[^@[:space:]\"']+@sha256:[0-9a-f]{64}" \
  | sed -E 's/:[^/@]+@/@/' \
  | grep -v "^${reg_re}/cozystack-packages@" \
  | sort -u)" || true
[ -n "$refs" ] || { echo "no ${REGISTRY}/ refs found under ${ROOT}; is this a tree the build stamped?" >&2; exit 1; }

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

missing=""
images=0
documents=0
# fd 3, so a registry client that reads stdin cannot swallow the list.
while IFS= read -r ref <&3; do
  name="${ref%@*}"
  name="${name##*/}"
  raw="$(docker buildx imagetools inspect "$ref" --raw)"
  sbom="$(docker buildx imagetools inspect "$ref" --format '{{ json .SBOM }}')"
  # The platforms the image serves are its index entries bar the attestation
  # manifests, keyed the way buildx keys them (arm64/v8 is plain arm64). buildx
  # prints the SBOM of an image with one platform as the document itself and
  # keys it by platform otherwise, so every platform of the index needs a
  # non-null document: "<file> <key>" per document, "- <platform>" per gap.
  # No output at all counts as no SBOM, not as an image with nothing to check.
  plan="$(printf '%s' "$sbom" | jq -rn --arg name "$name" --argjson raw "$raw" '
    [inputs][0] as $sbom
    | [ $raw.manifests[]?
        | select(.annotations["vnd.docker.reference.type"] != "attestation-manifest")
        | .platform
        | "\(.os)/\(.architecture)" + (if (.variant // "") == "" or (.architecture == "arm64" and .variant == "v8") then "" else "/" + .variant end)
      ] as $platforms
    | if ($platforms | length) <= 1 then
        if ($sbom | type) == "object" and ($sbom.SPDX | type) == "object" then "\($name).spdx.json ."
        else "- \($platforms[0] // "its platform")" end
      else
        $platforms[] as $p
        | if ($sbom | type) == "object" and ($sbom[$p].SPDX | type) == "object" then "\($name).\($p | gsub("/"; "-")).spdx.json \($p)"
          else "- \($p)" end
      end')"
  gaps="$(printf '%s\n' "$plan" | sed -n 's/^- //p' | paste -sd, -)"
  if [ -n "$gaps" ]; then
    missing="${missing}${ref} ${gaps}"$'\n'
    continue
  fi
  images=$((images + 1))
  while read -r file key; do
    if [ "$key" = . ]; then
      printf '%s' "$sbom" | jq '.SPDX' >"$stage/$file"
    else
      printf '%s' "$sbom" | jq --arg p "$key" '.[$p].SPDX' >"$stage/$file"
    fi
    documents=$((documents + 1))
  done <<<"$plan"
done 3<<<"$refs"

if [ -n "$missing" ]; then
  while read -r ref gaps; do
    [ -n "$ref" ] || continue
    echo "::error::${ref} carries no SBOM attestation for ${gaps}; was it built with SBOM=1?" >&2
  done <<<"$missing"
  exit 1
fi

tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$stage" -cf - . | gzip -n >"$ARCHIVE"
echo "image-sboms: ${documents} documents from ${images} images in ${ARCHIVE}"
