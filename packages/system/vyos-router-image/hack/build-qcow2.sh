#!/usr/bin/env bash
# Build the cozystack site-router appliance qcow2 from a published VyOS Stream
# image, and drop it at _out/assets/vyos-router-<arch>.qcow2.
#
# Two steps, and neither touches a VyOS package repository:
#
#   1. vyos-build's raw_image.py installs the published ISO onto a disk image.
#      `--reuse-iso` short-circuits live-build, so the flavor contributes only
#      image_format, disk_size and boot_settings — everything else it used to
#      declare is inert and has been removed from it.
#   2. hack/inject-appliance.sh adds what the stock image lacks: the guest agent
#      (from Debian, which the image is built on), the seed unit that installs
#      the per-instance configuration, the baked default configuration, and the
#      nginx log directory.
#
# This replaced a full live-build against packages.vyos.net/repositories/rolling.
# That repository keeps only the current kernel and republishes without warning,
# which made the build fail on upstream's schedule rather than on any change of
# ours; the per-release repositories that would have fixed it are not public.
# Stream images are archived, so the pin in the Makefile stays buildable.
set -euo pipefail

: "${VYOS_BUILD_IMAGE:?set by the Makefile}"
: "${VYOS_BUILD_REF:?set by the Makefile}"
: "${VYOS_VERSION:?set by the Makefile}"
: "${VYOS_ISO_URL:?set by the Makefile}"
: "${VYOS_ISO_SHA256:?set by the Makefile}"
: "${VYOS_DEB_URLS:?set by the Makefile}"
: "${VYOS_DEB_SHA256:?set by the Makefile}"
VYOS_ARCH="${VYOS_ARCH:-amd64}"

PKG_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "${PKG_DIR}/../../.." && pwd)"
FLAVOR_FILE="${PKG_DIR}/flavors/vyos-router.toml"
OVERLAY_DIR="${PKG_DIR}/overlay"
OUT_DIR="${REPO_ROOT}/_out/assets"
WORK_DIR="${REPO_ROOT}/_out/vyos-build"
CACHE_DIR="${REPO_ROOT}/_out/vyos-cache"
DEST="${OUT_DIR}/vyos-router-${VYOS_ARCH}.qcow2"

mkdir -p "${OUT_DIR}" "${CACHE_DIR}"

# Fetch and verify every downloaded input up front, so a bad or truncated
# download fails here rather than halfway through a privileged build.
fetch() {
    local url="$1" sum="$2" dest="$3"
    if [ ! -f "$dest" ] || ! echo "${sum}  ${dest}" | sha256sum -c - >/dev/null 2>&1; then
        echo "I: fetching $(basename "$dest")"
        curl -fL --retry 3 -o "$dest" "$url"
    fi
    echo "${sum}  ${dest}" | sha256sum -c -
}

ISO="${CACHE_DIR}/vyos-${VYOS_VERSION}-${VYOS_ARCH}.iso"
fetch "${VYOS_ISO_URL}" "${VYOS_ISO_SHA256}" "${ISO}"

DEB_DIR="${CACHE_DIR}/debs"
mkdir -p "${DEB_DIR}"
# Positionally paired lists: the Makefile keeps each URL beside its digest.
read -r -a _urls <<< "${VYOS_DEB_URLS}"
read -r -a _sums <<< "${VYOS_DEB_SHA256}"
if [ "${#_urls[@]}" -ne "${#_sums[@]}" ]; then
    echo "E: VYOS_DEB_URLS and VYOS_DEB_SHA256 have different lengths" >&2
    exit 1
fi
for i in "${!_urls[@]}"; do
    fetch "${_urls[$i]}" "${_sums[$i]}" "${DEB_DIR}/$(basename "${_urls[$i]%%\?*}")"
done

# The vyos-build checkout supplies raw_image.py and the flavor directory.
if [ ! -d "${WORK_DIR}/.git" ]; then
  rm -rf "${WORK_DIR}"
  git clone --filter=blob:none https://github.com/vyos/vyos-build.git "${WORK_DIR}"
fi
git -C "${WORK_DIR}" fetch --depth 1 origin "${VYOS_BUILD_REF}"
git -C "${WORK_DIR}" checkout --detach "${VYOS_BUILD_REF}"
# A previous run leaves root-owned build artifacts behind, which `git clean`
# cannot remove as the invoking user.
sudo rm -rf "${WORK_DIR}/build"
git -C "${WORK_DIR}" clean -xdf -e build

cp "${FLAVOR_FILE}" "${WORK_DIR}/data/build-flavors/vyos-router.toml"
cp "${ISO}" "${WORK_DIR}/appliance.iso"
mkdir -p "${WORK_DIR}/cozy-overlay" "${WORK_DIR}/cozy-debs"
cp "${OVERLAY_DIR}"/* "${WORK_DIR}/cozy-overlay/"
cp "${DEB_DIR}"/*.deb "${WORK_DIR}/cozy-debs/"
cp "${PKG_DIR}/hack/inject-appliance.sh" "${WORK_DIR}/cozy-inject.sh"
# cozy-inject.sh resolves this next to itself, so the two have to travel together.
cp "${PKG_DIR}/hack/grub-unrestrict.sed" "${WORK_DIR}/grub-unrestrict.sed"

# --privileged and -v /dev are required for the loop and overlay operations in
# both the conversion and the injection. build-vyos-image itself must run under
# sudo (its losetup/kpartx steps assume root and it does not sudo them), so its
# outputs are root-owned; the trailing chown hands the tree back to the invoking
# user so the mv below and any later clean can touch them.
#
# build-vyos-image unconditionally generates an SBOM with syft and the pinned
# container does not ship it, so fetch a version-pinned release and verify it
# against a repo-embedded digest rather than piping a remote script into a shell.
docker run --rm -i \
  --privileged \
  -v /dev:/dev \
  -v "${WORK_DIR}:/vyos" \
  -w /vyos \
  "${VYOS_BUILD_IMAGE}" \
  bash -c 'set -e
    curl -fsSL -o /tmp/syft.tgz https://github.com/anchore/syft/releases/download/v1.49.0/syft_1.49.0_linux_amd64.tar.gz
    echo "7aa2f03ee92739cf643279ba3990548b9925d4e22cae13f46831ee62821147fe  /tmp/syft.tgz" | sha256sum -c -
    sudo tar -xzf /tmp/syft.tgz -C /usr/local/bin syft
    sudo ./build-vyos-image --architecture "$1" --version "$2" --reuse-iso /vyos/appliance.iso vyos-router
    RAW=$(find /vyos/build -maxdepth 1 -name "*.raw" -print -quit)
    [ -n "$RAW" ] || { echo "E: no raw image produced" >&2; exit 1; }
    sudo /vyos/cozy-inject.sh "$RAW" /vyos/cozy-overlay /vyos/cozy-debs
    sudo qemu-img convert -f raw -O qcow2 "$RAW" "${RAW%.raw}.qcow2"
    sudo chown -R "$(id -u):$(id -g)" .' \
  -- "${VYOS_ARCH}" "${VYOS_VERSION}"

QCOW2_SRC="$(find "${WORK_DIR}/build" -maxdepth 1 -name '*.qcow2' -print -quit)"
if [ -z "${QCOW2_SRC}" ]; then
  echo "E: no qcow2 produced under ${WORK_DIR}" >&2
  exit 1
fi
mv -f "${QCOW2_SRC}" "${DEST}"
echo "I: VyOS router qcow2 ready at ${DEST}"
