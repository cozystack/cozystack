#!/bin/bash
set -e

terminate() {
  echo "Caught signal, terminating"
  exit 0
}

trap terminate SIGINT SIGQUIT SIGTERM

echo "Starting Linstor per-satellite plunger"

INTERVAL_SEC="${INTERVAL_SEC:-30}"
STALL_ITERS="${STALL_ITERS:-4}"
STATE_FILE="${STATE_FILE:-/run/drbd-sync-watch.state}"

log() { printf '%s %s\n' "$(date -Is)" "$*" >&2; }

drbd_status_json() {
  drbdsetup status --json 2>/dev/null || true
}

# Detect DRBD resources where resync is stuck:
# - at least one local device is Inconsistent
# - there is an active SyncTarget peer
# - there are other peers suspended with resync-suspended:dependency
# Output format: "<resource> <sync-peer> <percent-in-sync>"
drbd_stall_candidates() {
  jq -r '
    .[]?
    | . as $r
    | select(any($r.devices[]?; ."disk-state" == "Inconsistent"))
    | (
        [ $r.connections[]?
          | . as $c
          | $c.peer_devices[]?
          | select(."replication-state" == "SyncTarget")
          | { peer: $c.name, pct: (."percent-in-sync" // empty) }
        ] | .[0]?
      ) as $sync
    | select($sync != null and ($sync.pct|tostring) != "")
    | select(any($r.connections[]?.peer_devices[]?; ."resync-suspended" == "dependency"))
    | "\($r.name) \($sync.peer) \($sync.pct)"
  '
}

drbd_stall_load_state() {
  [ -f "$STATE_FILE" ] && cat "$STATE_FILE" || true
}

drbd_stall_save_state() {
  local tmp="${STATE_FILE}.tmp"
  cat >"$tmp"
  mv "$tmp" "$STATE_FILE"
}

# Break stalled resync by disconnecting the current SyncTarget peer.
# After reconnect, DRBD will typically pick another eligible peer and continue syncing.
drbd_stall_act() {
  local res="$1"
  local peer="$2"
  local pct="$3"
  log "STALL detected: res=$res sync_peer=$peer percent_in_sync=$pct -> disconnect/connect"
  drbdadm disconnect "${res}:${peer}" && drbdadm connect "$res" || log "WARN: action failed for ${res}:${peer}"
}

# Track percent-in-sync progress across iterations.
# If progress does not change for STALL_ITERS loops, trigger reconnect.
drbd_fix_stalled_sync() {
  local now prev json out
  now="$(date +%s)"
  prev="$(drbd_stall_load_state)"

  json="$(drbd_status_json)"
  [ -n "$json" ] || return 0

  out="$(printf '%s' "$json" | drbd_stall_candidates)"

  local new_state=""
  local acts=""

  while IFS= read -r line; do
    [ -n "$line" ] || continue
    set -- $line
    local res="$1" peer="$2" pct="$3"
    local key="${res} ${peer}"

    local prev_line
    prev_line="$(printf '%s\n' "$prev" | awk -v k="$key" '$1" "$2==k {print; exit}')"

    local cnt last_act prev_pct prev_cnt prev_act
    if [ -n "$prev_line" ]; then
      set -- $prev_line
      prev_pct="$3"
      prev_cnt="$4"
      prev_act="$5"
      if [ "$pct" = "$prev_pct" ]; then
        cnt=$((prev_cnt + 1))
      else
        cnt=1
      fi
      last_act="$prev_act"
    else
      cnt=1
      last_act=0
    fi

    if [ "$cnt" -ge "$STALL_ITERS" ]; then
      acts="${acts}${res} ${peer} ${pct}"$'\n'
      cnt=0
      last_act="$now"
    fi

    new_state="${new_state}${res} ${peer} ${pct} ${cnt} ${last_act}"$'\n'
  done <<< "$out"

  if [ -n "$acts" ]; then
    while IFS= read -r a; do
      [ -n "$a" ] || continue
      set -- $a
      drbd_stall_act "$1" "$2" "$3"
    done <<< "$acts"
  fi

  printf '%s' "$new_state" | drbd_stall_save_state
}

while true; do

  # timeout at the start of the loop to give a chance for the fresh linstor-satellite instance to cleanup itself
  sleep "$INTERVAL_SEC" &
  pid=$!
  wait $pid

  # Detect orphaned loop devices and detach them
  # the `/` path could not be a backing file for a loop device, so it's a good indicator of a stuck loop device
  # TODO describe the issue in more detail
  # Using the direct /usr/sbin/losetup as the linstor-satellite image has own wrapper in /usr/local
  stale_loopbacks=$(/usr/sbin/losetup --json | jq -r '.[][] | select(."back-file" == "/" or ."back-file" == "/ (deleted)").name')
  for stale_device in $stale_loopbacks; do (
    echo "Detaching stuck loop device ${stale_device}"
    set -x
    /usr/sbin/losetup --detach "${stale_device}" || echo "Command failed"
  ); done

  # Detect secondary volumes that got suspended with force-io-failure
  # As long as this is not a primary volume, it's somewhat safe to recreate the whole DRBD device.
  # Backing block device is not touched.
  disconnected_secondaries=$(drbdadm status 2>/dev/null | awk '/pvc-.*role:Secondary.*force-io-failures:yes/ {print $1}')
  for secondary in $disconnected_secondaries; do (
    echo "Trying to recreate secondary volume ${secondary}"
    set -x
    drbdadm down "${secondary}" || echo "Command failed"
    drbdadm up "${secondary}" || echo "Command failed"
  ); done

  # Detect and fix stalled DRBD resync by switching SyncTarget peer
  drbd_fix_stalled_sync || true

done
