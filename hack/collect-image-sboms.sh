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
# `docker buildx imagetools inspect` reads it back from the registry. The
# archive holds <image>.<os>-<arch>.spdx.json per platform of each image, or
# <image>.spdx.json for an image whose SBOM carries no platform key, so a user
# gets every document from one signed download rather than one registry read
# per image. The packages artifact is not an image and is left out.
#
# An image without an SBOM fails the run, naming it: every first-party image
# of a release is built with SBOM=1, so a missing one is a build that lost its
# attestation, not an image that cannot carry one.
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
  sbom="$(docker buildx imagetools inspect "$ref" --format '{{ json .SBOM }}')"
  # A multi-platform index answers with one document per platform under its
  # platform key; a single image answers with the document itself.
  files="$(printf '%s' "$sbom" | jq -r --arg name "$name" '
    if type != "object" then empty
    elif has("SPDX") then [$name + ".spdx.json"]
    else [to_entries[] | select(.value | type == "object" and has("SPDX")) | $name + "." + (.key | gsub("/"; "-")) + ".spdx.json"]
    end | .[]')"
  if [ -z "$files" ]; then
    missing="${missing}${missing:+ }${ref}"
    continue
  fi
  images=$((images + 1))
  while IFS= read -r file; do
    case "$file" in
      "${name}.spdx.json") printf '%s' "$sbom" | jq '.SPDX' >"$stage/$file" ;;
      *)
        platform="${file#"${name}".}"
        platform="${platform%.spdx.json}"
        printf '%s' "$sbom" | jq --arg p "$platform" '[to_entries[] | select((.key | gsub("/"; "-")) == $p)][0].value.SPDX' >"$stage/$file"
        ;;
    esac
    documents=$((documents + 1))
  done <<<"$files"
done 3<<<"$refs"

if [ -n "$missing" ]; then
  for ref in $missing; do
    echo "::error::${ref} carries no SBOM attestation; was it built with SBOM=1?" >&2
  done
  exit 1
fi

tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
  -C "$stage" -cf - . | gzip -n >"$ARCHIVE"
echo "image-sboms: ${documents} documents from ${images} images in ${ARCHIVE}"
