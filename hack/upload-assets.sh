#!/bin/bash
set -xe

version=${VERSION:-$(git describe --tags)}

assets=(
  _out/assets/cozystack-crds.yaml
  _out/assets/cozystack-operator-talos.yaml
  _out/assets/cozystack-operator-generic.yaml
  _out/assets/cozystack-operator-hosted.yaml
)
for arch in amd64 arm64; do
  assets+=(
    "_out/assets/metal-$arch.iso"
    "_out/assets/metal-$arch.raw.xz"
    "_out/assets/nocloud-$arch.raw.xz"
    "_out/assets/kernel-$arch"
    "_out/assets/initramfs-metal-$arch.xz"
  )
done
assets+=(
  _out/assets/cozypkg-*.tar.gz
  _out/assets/cozypkg-checksums.txt
  _out/assets/openapi.json
)

# Before the first upload, so that a release never carries assets without the
# signature over them: if signing fails, nothing is uploaded.
hack/sign-release-assets.sh "$version" "${assets[@]}"

for asset in "${assets[@]}" \
  _out/assets/cozystack-sbom.spdx.json \
  _out/assets/cozystack-checksums.txt \
  _out/assets/cozystack-checksums.txt.sigstore.json; do
  gh release upload --clobber "$version" "$asset"
done
