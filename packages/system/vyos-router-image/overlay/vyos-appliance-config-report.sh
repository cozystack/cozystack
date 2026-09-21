#!/bin/sh
# Say what vyos-router made of the configuration, on the console.
#
# A rejected config.boot reaches the console as the single line "Configuration
# error". On this appliance nothing can go and read the reason: there is no SSH
# service, the `vyos` login is locked, and the bootloader is password-protected.
# The bring-up emitter does not help either, because cron installs it from the
# configuration that just failed to load. So the console is the only channel and
# it carries one useless line.
#
# WHERE THE REASON ACTUALLY IS. Not in a file. vyos-boot-config-loader.py catches
# ConfigSessionError, writes the status file and exits before the block that
# writes its own log, so that log exists only on success. What does persist is
# the journal: conf_mode/pki.py and conf_mode/service_https.py are both dispatched
# through vyos-configd, which logs the ConfigError it caught. That is readable
# today with no kernel argument and no image debug flag.
#
# WHY IT POLLS. vyos-router.service is Type=simple, so systemd treats it as
# started the moment the process forks and an `After=` on it is satisfied long
# before the configuration is loaded — measured: an earlier revision of this
# script printed its line at boot and the "Configuration error" arrived seconds
# later. Waiting on the status file is what upstream's own vyos-config init does.
set -u

STATUS_FILE=/tmp/vyos-config-status
WAIT=240
MAX=40

log() { echo "vyos-appliance-config-report: $*" > /dev/console 2>&1; }

dump() {
    # -o cat drops the syslog preamble; the message is the whole point and the
    # console is narrow.
    journalctl -b -u "$1" -p err --no-pager -o cat 2>/dev/null | tail -n "$MAX" \
        | while IFS= read -r line; do
            [ -n "$line" ] && log "| $1: $line"
        done
}

# Whether the bring-up emitter is actually going to run. The seed installs it
# from the cozydiag disk, and it has been silent on every run so far even on the
# one where the seed reported installing it — so report the things that decide
# it rather than inferring from silence again. Two stats and a service check, on
# a path that already exists for reporting.
diag_inventory() {
    for f in /config/scripts/cozy-guest-diag.sh /etc/cron.d/cozy-guest-diag; do
        if [ -e "$f" ]; then
            log "diag: $f present ($(stat -c '%A %U:%G %s' "$f" 2>/dev/null))"
        else
            log "diag: $f MISSING"
        fi
    done
    # /config is an alias for the persistent config directory, and vyos-router
    # touches it during start(), after the seed has written there. Resolve it
    # rather than trusting the path.
    log "diag: /config -> $(readlink -f /config 2>/dev/null || echo unresolved)"
    if systemctl is-active --quiet cron 2>/dev/null; then
        log "diag: cron active"
    else
        log "diag: cron NOT active"
    fi
}

i=0
while [ ! -f "$STATUS_FILE" ] && [ "$i" -lt "$WAIT" ]; do
    i=$((i + 1))
    sleep 1
done

if [ ! -f "$STATUS_FILE" ]; then
    log "no $STATUS_FILE after ${WAIT}s; the config loader never reported. Journal follows."
    dump vyos-configd
    dump vyos-router
    exit 0
fi

status="$(cat "$STATUS_FILE" 2>/dev/null | tr -d '[:space:]')"
if [ "$status" = "0" ]; then
    log "configuration loaded and committed"
    diag_inventory
    exit 0
fi

log "configuration REJECTED (status ${status:-unknown}); reason follows"
dump vyos-configd
dump vyos-router
diag_inventory
exit 0
