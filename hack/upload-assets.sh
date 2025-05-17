#!/bin/bash
set -xe

version=${VERSION:-$(git describe --tags)}

gh release upload --clobber $version _out/assets/cozystack-installer.yaml
gh release upload --clobber $version _out/assets/metal-amd64.iso
gh release upload --clobber $version _out/assets/metal-amd64.raw.xz
gh release upload --clobber $version _out/assets/nocloud-amd64.raw.xz
gh release upload --clobber $version _out/assets/kernel-amd64
gh release upload --clobber $version _out/assets/initramfs-metal-amd64.xz
