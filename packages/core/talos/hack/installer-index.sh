#!/bin/sh
# installer-index.sh <oci-layout> <arch>...
#
# Adds to an OCI layout an image index, tagged "index", over the manifests
# tagged with each given arch. skopeo copies an index but cannot assemble one,
# and buildx can only take an OCI layout as a source on a builder the release
# job, which builds on the classic docker driver, does not have.
set -eu

layout=$1
shift

manifests=$(for arch in "$@"; do
  jq -c --arg arch "$arch" '.manifests[]
    | select(.annotations["org.opencontainers.image.ref.name"] == $arch)
    | {mediaType, digest, size, platform: {architecture: $arch, os: "linux"}}' "$layout/index.json"
done | jq -s -c .)

found=$(printf '%s' "$manifests" | jq length)
if [ "$found" -ne "$#" ]; then
  echo "installer-index.sh: $layout holds $found of the manifests for: $*" >&2
  exit 1
fi

index=$(jq -n -c --argjson m "$manifests" \
  '{schemaVersion: 2, mediaType: "application/vnd.oci.image.index.v1+json", manifests: $m}')
digest=$(printf '%s' "$index" | sha256sum | cut -d' ' -f1)
size=$(printf '%s' "$index" | wc -c | tr -d ' ')
printf '%s' "$index" > "$layout/blobs/sha256/$digest"

jq --arg d "sha256:$digest" --argjson s "$size" \
  '.manifests += [{mediaType: "application/vnd.oci.image.index.v1+json", digest: $d, size: $s,
       annotations: {"org.opencontainers.image.ref.name": "index"}}]' \
  "$layout/index.json" > "$layout/index.json.new"
mv "$layout/index.json.new" "$layout/index.json"
