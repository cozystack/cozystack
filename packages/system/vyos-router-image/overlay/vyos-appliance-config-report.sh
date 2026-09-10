#!/bin/sh
# Print why the configuration failed, to the console, after vyos-router has tried
# to load it.
#
# vyos-router reports a rejected config.boot as the single line "Configuration
# error" and puts the reason in /var/log/vyatta/vyos-config.log inside the guest.
# On this appliance nothing can go and read that: there is no SSH service, the
# `vyos` login is locked, and the bootloader is password-protected, so the
# console is the only channel and it carries the one useless line. The bring-up
# emitter does not help either, because it is installed by the configuration that
# just failed to load.
#
# So this runs after vyos-router regardless of outcome and puts the tail on the
# console, where logSerialConsole and the e2e collector already pick it up. It is
# a few lines on a successful boot and the whole answer on a failed one.
set -u

LOG=/var/log/vyatta/vyos-config.log
MAX=25

log() { echo "vyos-appliance-config-report: $*" > /dev/console 2>&1; }

if [ ! -s "$LOG" ]; then
    log "no $LOG; vyos-router either did not run or wrote nothing"
    exit 0
fi

log "last $MAX lines of $LOG:"
tail -n "$MAX" "$LOG" 2>/dev/null | while IFS= read -r line; do
    log "| $line"
done
exit 0
