#!/bin/bash
set -xe

version=${VERSION:-$(git describe --tags)}

gh release upload --clobber $version _out/assets/cozystack-crds.yaml
gh release upload --clobber $version _out/assets/cozystack-operator.yaml
gh release upload --clobber $version _out/assets/cozystack-operator-generic.yaml
gh release upload --clobber $version _out/assets/cozystack-operator-hosted.yaml
gh release upload --clobber $version _out/assets/metal-amd64.iso
gh release upload --clobber $version _out/assets/metal-amd64.raw.xz
gh release upload --clobber $version _out/assets/nocloud-amd64.raw.xz
gh release upload --clobber $version _out/assets/kernel-amd64
gh release upload --clobber $version _out/assets/initramfs-metal-amd64.xz
gh release upload --clobber $version _out/assets/cozypkg-*.tar.gz
gh release upload --clobber $version _out/assets/cozypkg-checksums.txt
