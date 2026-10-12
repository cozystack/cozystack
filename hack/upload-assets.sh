#!/bin/bash
set -xe

version=${VERSION:-$(git describe --tags)}

gh release upload --clobber $version _out/assets/cozystack-crds.yaml
gh release upload --clobber $version _out/assets/cozystack-operator-talos.yaml
gh release upload --clobber $version _out/assets/cozystack-operator-generic.yaml
gh release upload --clobber $version _out/assets/cozystack-operator-hosted.yaml
for arch in amd64 arm64; do
  gh release upload --clobber $version _out/assets/metal-$arch.iso
  gh release upload --clobber $version _out/assets/metal-$arch.raw.xz
  gh release upload --clobber $version _out/assets/nocloud-$arch.raw.xz
  gh release upload --clobber $version _out/assets/kernel-$arch
  gh release upload --clobber $version _out/assets/initramfs-metal-$arch.xz
done
gh release upload --clobber $version _out/assets/cozypkg-*.tar.gz
gh release upload --clobber $version _out/assets/cozypkg-checksums.txt
gh release upload --clobber $version _out/assets/openapi.json
