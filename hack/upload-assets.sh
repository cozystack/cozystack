#!/bin/bash
set -xe

version=$(git describe --tags)
gh release upload --clobber $version _out/assets/cozystack-installer.yaml
gh release upload --clobber $version _out/assets/metal-amd64.iso
gh release upload --clobber $version _out/assets/metal-amd64.raw.xz
gh release upload --clobber $version _out/assets/nocloud-amd64.raw.xz
