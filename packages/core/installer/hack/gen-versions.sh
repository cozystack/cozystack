#!/bin/sh

FIRMWARES="amd-ucode amdgpu-firmware bnx2-bnx2x i915-ucode intel-ice-firmware intel-ucode qlogic-firmware"
EXTENSIONS="drbd zfs"

talos_version=${1:-$(skopeo --override-os linux --override-arch amd64 list-tags docker://ghcr.io/siderolabs/imager | jq -r '.Tags[]' | grep '^v[0-9]\+.[0-9]\+.[0-9]\+$' | sort -V | tail -n 1)}

echo "TALOS_VERSION=$talos_version"

for firmware in $FIRMWARES; do
  firmware_var=$(echo "$firmware" | tr '[:lower:]' '[:upper:]' | tr - _)_VERSION
  version=$(skopeo list-tags docker://ghcr.io/siderolabs/$firmware | jq -r '.Tags[]|select(length == 8)|select(startswith("20"))' | sort -V | tail -n 1)
  echo "$firmware_var=$version"
done

for extension in $EXTENSIONS; do
  extension_var=$(echo "$extension" | tr '[:lower:]' '[:upper:]' | tr - _)_VERSION
  version=$(skopeo --override-os linux --override-arch amd64 list-tags docker://ghcr.io/siderolabs/$extension | jq -r '.Tags[]' | grep "\-${talos_version}$" | sort -V | tail -n1)
  echo "$extension_var=$version"
done
