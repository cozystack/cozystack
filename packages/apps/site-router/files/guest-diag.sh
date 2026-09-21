#!/bin/sh
# Bring-up diagnostics for the VyOS gateway guest, emitted to the serial console.
#
# WHY THE CONSOLE, AND NOT ANYTHING ELSE. The appliance has exactly one channel
# that does not depend on the guest reaching a working service: the serial
# console. There is no SSH service and the baked `vyos` login is locked
# (flavors/vyos-router.toml), the HTTPS management API is the thing whose failure
# this exists to explain, and qemu-guest-agent guest-exec is a deliberate Phase-1
# non-goal (see the DELIVERY MECHANISM note in templates/_helpers.tpl). The
# flavor puts `console=ttyS0,115200` on the kernel cmdline (boot_settings
# console_type = "ttyS"), so /dev/console reaches ttyS0 — the port KubeVirt's
# logSerialConsole streams into the guest-console-log container. If ttyS0 were
# not a registered console the whole feature would capture nothing, kernel boot
# messages included, so writing here is safe to rely on rather than probe for.
#
# WHY CRON DRIVES IT. cloud-init on this image runs `write_files` and nothing
# executable: cloud_final_modules is empty, so runcmd/bootcmd/scripts-per-boot
# never run. Of the ways to get a script started after write_files without a
# maintainer-run appliance rebuild, only cron works:
#   - /etc/rc.local is decided by systemd-rc-local-generator, which runs in early
#     boot BEFORE cloud-init writes anything, so a file dropped later is ignored
#     for this boot.
#   - a unit plus an /etc/systemd/system/*.wants entry needs `daemon-reload` and
#     a target restart, because the multi-user.target transaction is already
#     computed by the time write_files runs.
#   - VyOS `system task-scheduler` lives in config.boot, so it only exists once
#     the config COMMITS — which is one of the things being diagnosed. Scheduling
#     the diagnostic through the mechanism under investigation is self-defeating.
#   - cron rescans /etc/cron.d every minute regardless of when a file appears,
#     needs no reload, and is running by construction (VyOS's own task-scheduler
#     is built on it).
# Nothing here touches config.boot, so a mistake in this script cannot stop the
# guest configuring itself.
#
# Every command is wrapped in `timeout`: a diagnostic that hangs is worse than no
# diagnostic, and the box this runs on is suspected of being wedged.
#
# Overlapping runs are tolerated rather than locked out. Each line carries the
# uptime and the emitting pid, so two interleaved runs stay readable, and that
# costs nothing and cannot leave a stale lock behind on a box that is already the
# subject of the investigation.

set -u

# Overridable so the bats suite can run this against a temp file and a stubbed
# PATH instead of a real guest. Defaults are the production values.
COZY_DIAG_CONSOLE="${COZY_DIAG_CONSOLE:-/dev/console}"
# Emit only during bring-up. The suite's three inner loops span 900s, so 20
# minutes covers the whole window with headroom; after that every cron tick exits
# in a few milliseconds, which is what keeps this from being console noise for the
# life of a long-running gateway.
COZY_DIAG_WINDOW="${COZY_DIAG_WINDOW:-1200}"
# Samples per cron tick, and the gap between them. 2 x 20s puts the worst case
# (two samples of ~19s of timeouts plus one 20s sleep) just inside the minute, so
# a tick normally finishes before the next one starts.
COZY_DIAG_SAMPLES="${COZY_DIAG_SAMPLES:-2}"
COZY_DIAG_INTERVAL="${COZY_DIAG_INTERVAL:-20}"

TAG='[cozy-diag]'

uptime_secs() {
  # Integer seconds since boot. Substring rather than awk so the cheapest read in
  # the script does not depend on a second binary.
  read -r _up _idle < /proc/uptime 2>/dev/null || _up=0
  echo "${_up%%.*}"
}

emit() {
  # Appended, never truncating, and failure-tolerant: if the console is gone the
  # diagnostic must not become the reason the guest is unhealthy.
  printf '%s t=%ss p=%s %s\n' "$TAG" "$(uptime_secs)" "$$" "$*" \
    >> "$COZY_DIAG_CONSOLE" 2>/dev/null || true
}

# emit_field LABEL BUDGET MAXLINES COMMAND... — run COMMAND under a `timeout` of
# BUDGET seconds and emit at most MAXLINES of its output under LABEL. stderr is
# folded in on purpose: for most of these the error text ("Unit
# strongswan.service could not be found") is the finding.
#
# MAXLINES is not tidiness. `ps`, `ss` and `journalctl` all produce output whose
# length is a property of the box being diagnosed, and this writes to a serial
# console that a human and a log collector both have to read: one field must not
# be able to bury the other twelve. Truncation is always announced, because a
# silently cut field reads as a complete one that found less.
emit_field() {
  _label="$1"
  _budget="$2"
  _max="$3"
  shift 3
  _out="$(timeout "$_budget" "$@" 2>&1)" || true
  if [ -z "$_out" ]; then
    emit "$_label: <empty>"
    return 0
  fi
  _n=0
  printf '%s\n' "$_out" | while IFS= read -r _line; do
    [ -n "$_line" ] || continue
    _n=$((_n + 1))
    if [ "$_n" -gt "$_max" ]; then
      emit "$_label: <truncated at $_max lines>"
      break
    fi
    emit "$_label: $_line"
  done
}

sample() {
  emit "---- sample begin ----"

  # 1. Did cloud-init finish, and how. Without this, everything below is being
  #    read against an unknown configuration state.
  emit_field cloud-init 3 8 cloud-init status --long

  # 2. Whole-system health, without naming units. `is-system-running` plus the
  #    failed list catches the config activation, nginx and strongSwan failing
  #    without this script having to guess any of their unit names — a guess that
  #    would report a healthy box as broken if the name were wrong.
  emit_field systemd 3 2 systemctl is-system-running
  emit_field failed-units 5 10 systemctl list-units --failed --no-legend --plain

  # 3. Did the seeded config actually commit. The seed is a file; the ACTIVE
  #    config is a directory tree, so the presence of the service/https node in
  #    it is direct evidence that cloud-init's config.boot was loaded rather than
  #    merely written. Reported as raw existence, because a missing tree means
  #    "this probe does not apply on this image" and must not be read as "the
  #    config did not commit".
  emit "seed-file: $( [ -f /opt/vyatta/etc/config/config.boot ] && echo present || echo ABSENT )"
  emit "active-tree: $( [ -d /opt/vyatta/config/active ] && echo present || echo absent-or-unknown-layout )"
  emit "active-https-node: $( [ -d /opt/vyatta/config/active/service/https ] && echo present || echo absent )"

  # 4. The management API listener. nginx is the :443 HTTPS-API listener on this
  #    appliance and it has a known hard failure mode (it will not start without
  #    /var/log/nginx, which the flavor creates precisely for that reason), so it
  #    is named explicitly rather than left to the generic listener dump.
  emit_field nginx 3 2 systemctl is-active nginx
  emit_field listeners 3 12 ss -lntH
  emit_field nginx-errors 3 3 tail -n 3 /var/log/nginx/error.log

  # 5. IPsec. Absent/inactive is the expected reading before the tunnel is
  #    pushed, so this is here to separate "not configured yet" from "configured
  #    and not coming up".
  emit_field ipsec-unit 3 2 systemctl is-active strongswan
  emit_field ipsec-sas 5 12 swanctl --list-sas

  # 6. Still working, or wedged. This is the reading the last failure could not
  #    make: both guests were CPU-saturated when the step gave up, and load plus
  #    the top consumers is what separates a slow boot from a hung service.
  emit_field loadavg 3 1 cat /proc/loadavg
  emit_field top-cpu 5 6 ps -eo pcpu,etime,comm --sort=-pcpu --no-headers

  # 7. Anything the kernel or a unit shouted. Last, and short, because it is the
  #    only field here that repeats itself across samples.
  emit_field errors 5 3 journalctl -b -p err --no-pager -n 3

  emit "---- sample end ----"
}

_now="$(uptime_secs)"
if [ "$_now" -gt "$COZY_DIAG_WINDOW" ]; then
  # Past the bring-up window: exit silently so a long-lived gateway pays a few
  # milliseconds a minute and puts nothing on the console.
  exit 0
fi

_i=0
while [ "$_i" -lt "$COZY_DIAG_SAMPLES" ]; do
  [ "$_i" -eq 0 ] || sleep "$COZY_DIAG_INTERVAL"
  sample
  _i=$((_i + 1))
done
exit 0
