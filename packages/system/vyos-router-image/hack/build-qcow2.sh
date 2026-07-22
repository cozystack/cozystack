#!/usr/bin/env bash
# Build the conformant VyOS 1.5-rolling "site-router" appliance qcow2 via the
# pinned vyos-build tooling, and drop it at _out/assets/vyos-router-amd64.qcow2.
#
# Mirrors the Talos disk build shape (a privileged `docker run` of an upstream
# imager into _out/assets), but VyOS builds a full live-build image rather than
# assembling from prebuilt layers, so it is heavier (debootstrap + apt + squashfs)
# and needs its scratch on a real local filesystem with several GiB free.
#
# Reproducibility levers (see the Makefile for the pinned values, exported here):
#   VYOS_BUILD_IMAGE  the vyos-build container, digest-pinned (the build TOOLING)
#   VYOS_BUILD_REF    the vyos-build git ref, commit-pinned (build-vyos-image +
#                     flavors + live-build-config)
#   VYOS_VERSION      the snapshot version label stamped into the artifact name
# The upstream rolling apt mirror (packages.vyos.net/repositories/rolling) floats,
# so this is pinned-inputs / best-effort reproducible, not bit-identical.
set -euo pipefail

: "${VYOS_BUILD_IMAGE:?set by the Makefile}"
: "${VYOS_BUILD_REF:?set by the Makefile}"
: "${VYOS_VERSION:?set by the Makefile}"
VYOS_ARCH="${VYOS_ARCH:-amd64}"

# Resolve repo-root-relative paths regardless of the caller's cwd.
PKG_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "${PKG_DIR}/../../.." && pwd)"
FLAVOR_FILE="${PKG_DIR}/flavors/vyos-router.toml"
OUT_DIR="${REPO_ROOT}/_out/assets"
WORK_DIR="${REPO_ROOT}/_out/vyos-build"
DEST="${OUT_DIR}/vyos-router-${VYOS_ARCH}.qcow2"

mkdir -p "${OUT_DIR}"

# Fetch the pinned vyos-build checkout (shallow; the tooling, flavors and
# live-build-config live here — vyos-1x is cloned by build-vyos-image itself).
if [ ! -d "${WORK_DIR}/.git" ]; then
  rm -rf "${WORK_DIR}"
  git clone --filter=blob:none https://github.com/vyos/vyos-build.git "${WORK_DIR}"
fi
git -C "${WORK_DIR}" fetch --depth 1 origin "${VYOS_BUILD_REF}"
git -C "${WORK_DIR}" checkout --detach "${VYOS_BUILD_REF}"
git -C "${WORK_DIR}" clean -xdf

# Register our flavor so the `vyos-router` positional argument resolves.
cp "${FLAVOR_FILE}" "${WORK_DIR}/data/build-flavors/vyos-router.toml"

# Build. --privileged is required for the loop/kpartx disk operations; -v /dev is
# passed for the same reason the Talos imager needs it. The checkout is bind-
# mounted at /vyos (per the vyos-build docs) so its build/ scratch lands on the
# host filesystem under _out/vyos-build.
docker run --rm -i \
  --privileged \
  -v /dev:/dev \
  -v "${WORK_DIR}:/vyos" \
  -w /vyos \
  "${VYOS_BUILD_IMAGE}" \
  sudo --preserve-env ./build-vyos-image \
    --architecture "${VYOS_ARCH}" \
    --version "${VYOS_VERSION}" \
    vyos-router

# build-vyos-image names the artifact vyos-<version>-vyos-router-<arch>.qcow2;
# glob for it rather than reconstructing the exact name (the version string can be
# normalised by the tooling).
QCOW2_SRC="$(find "${WORK_DIR}" -maxdepth 2 -name 'vyos-*-vyos-router-*.qcow2' -print -quit)"
if [ -z "${QCOW2_SRC}" ]; then
  echo "E: no qcow2 produced under ${WORK_DIR}" >&2
  exit 1
fi
mv -f "${QCOW2_SRC}" "${DEST}"
echo "I: VyOS router qcow2 ready at ${DEST}"
