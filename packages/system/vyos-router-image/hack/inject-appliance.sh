#!/usr/bin/env bash
# Turn the stock VyOS Stream disk into the cozystack site-router appliance.
#
# Runs INSIDE the privileged vyos-build container (see build-qcow2.sh), as root,
# against the .raw that build-vyos-image produced from the published ISO. The
# conversion step there is the only part of vyos-build still in play: --reuse-iso
# skips live-build entirely, so nothing the flavor declares beyond image_format,
# disk_size and boot_settings reaches the image, and everything the appliance
# needs on top has to be placed here.
#
# The disk is a VyOS union image: a read-only squashfs under /boot/<version>/ with
# an overlayfs upper directory beside it. Writing into that upper directory is how
# a file gets into the running system, so this assembles the same overlay VyOS
# assembles at boot and works inside it. The squashfs is never rebuilt.
set -euo pipefail

RAW="${1:?usage: inject-appliance.sh <raw image> <overlay dir> <deb dir>}"
OVERLAY_DIR="${2:?}"
DEB_DIR="${3:?}"

LOOP="" ; MNT=$(mktemp -d) ; LOW=$(mktemp -d) ; MERGED=$(mktemp -d)

cleanup() {
    umount "${MERGED}/sys" "${MERGED}/proc" "${MERGED}/dev" 2>/dev/null || true
    umount "$MERGED" 2>/dev/null || true
    umount "$LOW" 2>/dev/null || true
    umount "$MNT" 2>/dev/null || true
    [ -n "$LOOP" ] && losetup -d "$LOOP" 2>/dev/null || true
    rmdir "$MERGED" "$LOW" "$MNT" 2>/dev/null || true
}
trap cleanup EXIT

LOOP=$(losetup --show -fP "$RAW")
# p3 is the root partition create_raw_image lays down (p1 BIOS boot, p2 EFI).
mount "${LOOP}p3" "$MNT"

# The version directory is named after the image, and the image is the one thing
# here that is not ours to pin, so discover it rather than hardcode it.
VERSION_DIR=$(find "$MNT/boot" -mindepth 1 -maxdepth 1 -type d ! -name efi ! -name grub -print -quit)
[ -n "$VERSION_DIR" ] || { echo "E: no version directory under $MNT/boot" >&2; exit 1; }
echo "I: appliance version directory: ${VERSION_DIR#"$MNT"}"

SQUASHFS=$(find "$VERSION_DIR" -maxdepth 1 -name '*.squashfs' -print -quit)
[ -n "$SQUASHFS" ] || { echo "E: no squashfs in $VERSION_DIR" >&2; exit 1; }

mount -o ro,loop "$SQUASHFS" "$LOW"
mount -t overlay overlay \
    -o "lowerdir=${LOW},upperdir=${VERSION_DIR}/rw,workdir=${VERSION_DIR}/work" "$MERGED"

# dpkg needs a populated /dev, /proc and /sys to configure a package that ships a
# systemd unit; without them the postinst fails rather than merely warning.
mount --bind /dev "${MERGED}/dev"
mount -t proc proc "${MERGED}/proc"
mount -t sysfs sys "${MERGED}/sys"

# qemu-guest-agent is a Debian package, not a VyOS one, and the appliance is a
# Debian bookworm system, so it and its one missing dependency install from the
# frozen Debian archive. Installing through dpkg rather than unpacking the files
# keeps the package registered, so a later audit of the image sees it.
echo "I: installing guest agent packages"
mkdir -p "${MERGED}/tmp/debs"
cp "$DEB_DIR"/*.deb "${MERGED}/tmp/debs/"
chroot "$MERGED" sh -c 'dpkg -i /tmp/debs/*.deb'
rm -rf "${MERGED}/tmp/debs"
chroot "$MERGED" sh -c 'command -v qemu-ga >/dev/null' \
    || { echo "E: qemu-ga missing after install" >&2; exit 1; }

echo "I: installing the appliance seed and baked configuration"
install -m 0755 "${OVERLAY_DIR}/vyos-appliance-seed.sh" "${MERGED}/usr/local/sbin/vyos-appliance-seed.sh"
install -m 0644 "${OVERLAY_DIR}/vyos-appliance-seed.service" "${MERGED}/etc/systemd/system/vyos-appliance-seed.service"
install -d "${MERGED}/etc/systemd/system/vyos.target.wants"
ln -sf /etc/systemd/system/vyos-appliance-seed.service \
    "${MERGED}/etc/systemd/system/vyos.target.wants/vyos-appliance-seed.service"

install -d "${MERGED}/usr/share/vyos"
install -m 0644 "${OVERLAY_DIR}/config.boot.default" "${MERGED}/usr/share/vyos/config.boot.default"

# nginx serves the HTTPS API the controller drives and will not start without
# its log directory.
install -d -m 0755 "${MERGED}/var/log/nginx"

sync
echo "I: appliance injection complete"
