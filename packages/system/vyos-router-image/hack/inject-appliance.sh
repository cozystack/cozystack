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

# The one regex that has to keep matching upstream's menuentry line shape lives
# in its own file so hack/grub-unrestrict.bats exercises this exact program
# rather than a copy of it.
UNRESTRICT_SED="$(dirname "$0")/grub-unrestrict.sed"
[ -f "$UNRESTRICT_SED" ] || { echo "E: $UNRESTRICT_SED not found" >&2; exit 1; }

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

# --- Lock the bootloader ----------------------------------------------------
#
# The gateway VM runs with OVN port_security relaxed, and the guest firewall is
# the only thing standing in for it (docs/security-model.md). That makes root in
# this guest equivalent to removing the compensating control, so every path to a
# root shell that does not go through the platform has to be closed — and the
# console is not out of tenant reach: cozy:tenant:use:base grants
# virtualmachineinstances/console, /vnc and virtualmachines/restart in every
# tenant namespace, which is where a SiteRouter lands.
#
# Stock VyOS hands that console a root shell in two keystrokes. It is not the
# generic press-e-and-edit path: data/templates/grub/grub_options.j2 ships a
# "Boot options" submenu whose "Password reset" entry boots
# init=/usr/libexec/vyos/system/standalone_root_pw_reset and whose "System
# recovery" entry boots init=/usr/bin/busybox init.
#
# Setting superusers is what closes all of it at once: GRUB then requires
# authentication for entry editing, for the command line and for every menu
# entry that is not explicitly marked --unrestricted. Which is also the trap —
# grub_vyos_version.j2 renders the normal-boot entry WITHOUT that flag, so
# superusers alone means the appliance stops booting unattended and asks a human
# for a password that does not exist. Verified in qemu: with superusers set and
# no --unrestricted, an untouched boot stops at "Enter username"; with the flag,
# the same config boots straight through.
#
# The password is random per build and deliberately not kept. An appliance-wide
# secret baked into a public image is not a secret, and the security model
# already says the break-glass credential is injected out of band rather than
# built in. So this locks the bootloader outright: normal boot works, and there
# is no console behind it for anyone.
#
# The two recovery menu entries are left in place. They are superuser-gated by
# the same setting, and removing them would not stick: grub_update.py re-renders
# 50-vyos-options.cfg from its template on a structure migration.
echo "I: locking the bootloader"

GRUB_CFG_D="$MNT/boot/grub/grub.cfg.d"
[ -d "$GRUB_CFG_D" ] || { echo "E: no $GRUB_CFG_D in the image" >&2; exit 1; }

command -v grub-mkpasswd-pbkdf2 >/dev/null \
    || { echo "E: grub-mkpasswd-pbkdf2 not in the build image; refusing to ship an unlocked bootloader" >&2; exit 1; }

GRUB_PW=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
GRUB_HASH=$(printf '%s\n%s\n' "$GRUB_PW" "$GRUB_PW" | grub-mkpasswd-pbkdf2 | sed -n 's/^PBKDF2 hash of your password is //p')
unset GRUB_PW
case "$GRUB_HASH" in
    grub.pbkdf2.sha512.*) ;;
    *) echo "E: grub-mkpasswd-pbkdf2 produced no usable hash" >&2; exit 1 ;;
esac

# The name matters twice. grub_main.j2 sources ONLY grub.cfg.d/*-autoload.cfg,
# so a file without that suffix is silently never read; and the numeric prefix
# has to sort before 40-vyos-menu-autoload.cfg, which is where the entries this
# protects are defined.
cat > "${GRUB_CFG_D}/05-cozy-superusers-autoload.cfg" <<EOF
# Managed by cozystack (packages/system/vyos-router-image/hack/inject-appliance.sh).
# The password is generated at build time and discarded; nothing can authenticate
# against it. That is deliberate — see the comment in that script.
set superusers="cozy-locked"
password_pbkdf2 cozy-locked ${GRUB_HASH}
EOF
chmod 0600 "${GRUB_CFG_D}/05-cozy-superusers-autoload.cfg"

# Mark the normal-boot entries bootable-by-anyone, in both places they come
# from: the entries already generated into the image, and the template a later
# structure migration would re-render them from.
for vercfg in "${GRUB_CFG_D}"/vyos-versions/*.cfg; do
    [ -e "$vercfg" ] || { echo "E: no version menu entries under ${GRUB_CFG_D}/vyos-versions" >&2; exit 1; }
    sed -i -E -f "$UNRESTRICT_SED" "$vercfg"
    grep -q -- '--unrestricted' "$vercfg" \
        || { echo "E: failed to mark $vercfg unrestricted; the appliance would not boot unattended" >&2; exit 1; }
done

VYOS_TMPL_DIR=$(chroot "$MERGED" python3 -c 'from vyos.defaults import directories; print(directories["templates"])')
VERSION_TMPL="${MERGED}${VYOS_TMPL_DIR}/grub/grub_vyos_version.j2"
[ -f "$VERSION_TMPL" ] || { echo "E: version menu template not found at $VERSION_TMPL" >&2; exit 1; }
sed -i -E -f "$UNRESTRICT_SED" "$VERSION_TMPL"
grep -q -- '--unrestricted' "$VERSION_TMPL" \
    || { echo "E: failed to patch $VERSION_TMPL; a structure migration would relock normal boot" >&2; exit 1; }

# GRUB reads a directory by inode order, not by name, so the numeric prefixes
# only mean anything after the files are recreated in name order. This mirrors
# vyos.system.grub.sort_inodes, which raw_image.py already ran before this file
# existed and which grub_update.py runs again after any migration.
sort_grub_inodes() {
    local dir="$1" f
    for f in $(printf '%s\n' "$dir"/* | sort); do
        [ -f "$f" ] || continue
        cp -p "$f" "${f}._reinode"
        rm -f "$f"
        mv "${f}._reinode" "$f"
    done
}
sort_grub_inodes "$GRUB_CFG_D"

sync
echo "I: appliance injection complete"
