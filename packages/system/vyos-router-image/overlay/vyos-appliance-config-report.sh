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
    exit 0
fi

log "configuration REJECTED (status ${status:-unknown}); reason follows"
dump vyos-configd
dump vyos-router
exit 0
