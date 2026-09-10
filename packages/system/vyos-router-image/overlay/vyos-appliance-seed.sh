#!/bin/sh
# Install the per-instance VyOS configuration from the NoCloud seed disk before
# vyos-router loads it.
#
# This replaces cloud-init, which this appliance does not ship. The chart's whole
# use of cloud-init was one `write_files` entry placing config.boot; none of the
# VyOS cloud-init modules were used (see the cloudInitUserData helper in
# packages/apps/site-router). Since the Stream image carries no cloud-init and
# VyOS's fork is only in a repository that is not public, reproducing that one
# file copy here removes the dependency instead of working around it.
#
# KubeVirt renders `cloudInitNoCloud.secretRef` into an ISO labelled `cidata`
# whose `user-data` is the Secret's userdata verbatim, so that file IS the
# config.boot to install — it carries the controller's API token and is therefore
# per-instance, which is why it cannot be baked into the image.
#
# Never fails the boot. A gateway that comes up on its baked default config is
# reachable over the serial console and can be diagnosed; one held in a failed
# unit is not, and the tunnel is down either way.
set -u
log() { echo "vyos-appliance-seed: $*" > /dev/console 2>&1; }

DEV=$(blkid -L cidata 2>/dev/null) || DEV=""
if [ -z "$DEV" ]; then
    log "no disk labelled cidata; leaving the baked default config in place"
    exit 0
fi

MNT=/run/vyos-appliance-seed
mkdir -p "$MNT"
if ! mount -o ro "$DEV" "$MNT" 2>/dev/null; then
    log "E: found $DEV but could not mount it; config not seeded"
    exit 0
fi

if [ -s "$MNT/user-data" ]; then
    mkdir -p /opt/vyatta/etc/config
    if cp "$MNT/user-data" /opt/vyatta/etc/config/config.boot; then
        chgrp vyattacfg /opt/vyatta/etc/config/config.boot 2>/dev/null || true
        chmod 0660 /opt/vyatta/etc/config/config.boot
        log "installed config.boot from $DEV"
    else
        log "E: copy from $DEV failed; config not seeded"
    fi
else
    log "E: $DEV carries no non-empty user-data; config not seeded"
fi

umount "$MNT" 2>/dev/null || true

# Optional diagnostics, on their own disk so that a broken payload here cannot
# touch the configuration above. The chart attaches it only when the platform or
# the e2e suite asked for the serial console; a tenant cannot.
DIAG=$(blkid -L cozydiag 2>/dev/null) || DIAG=""
if [ -z "$DIAG" ]; then
    # Say so. This branch used to be silent, and the label was empty for every
    # run because the chart set no volumeLabel on the Secret volume — KubeVirt
    # takes the iso9660 label from Secret.VolumeLabel, not from the volume name.
    # The result was a guest with no diagnostics and no hint that it had none,
    # on exactly the runs where the diagnostics were the thing being looked for.
    log "no disk labelled cozydiag; guest diagnostics not installed"
fi
if [ -n "$DIAG" ] && mount -o ro "$DIAG" "$MNT" 2>/dev/null; then
    if [ -s "$MNT/guest-diag.sh" ] && [ -s "$MNT/guest-diag.cron" ]; then
        mkdir -p /config/scripts
        install -m 0755 "$MNT/guest-diag.sh" /config/scripts/cozy-guest-diag.sh
        # No dot in the installed name: Debian cron silently skips /etc/cron.d
        # entries containing anything but letters, digits, underscore and hyphen.
        install -m 0644 "$MNT/guest-diag.cron" /etc/cron.d/cozy-guest-diag
        log "installed guest diagnostics from $DIAG"
    else
        log "E: $DIAG carries no usable diagnostics payload"
    fi
    umount "$MNT" 2>/dev/null || true
fi

exit 0
