#!/bin/bash
set -e

terminate() {
  echo "Caught signal, terminating"
  exit 0
}

trap terminate SIGINT SIGQUIT SIGTERM

function get_drbd_connecting() {
  all_devices="$(drbdsetup status --json 2>/dev/null)"
  unhealthy_devices="$(echo "${all_devices}" | jq -r '
  map(
    select(
      # Skip devices that were suspended for io errors, reconnect will not help here
      .suspended == false and
      # Uncomment to select Secondary devices only
      # .role == "Secondary" and
      (.connections[]."connection-state" == "Connecting")
    )
    | {
        name: .name,
        "peer-node-id": .connections[]
          | select(.["connection-state"] == "Connecting")
          | ."peer-node-id"
      }
  )
  # redundant, but required for array intersection calculation later
  | unique
  ')"
  echo "${unhealthy_devices}"
}

echo "Starting Linstor per-satellite plunger"

while true; do

  # timeout at the start of the loop to give a chance for the fresh linstor-satellite instance to cleanup itself
  sleep 30 &
  pid=$!
  wait $pid

  # Detect orphaned loop devices and detach them
  # the `/` path could not be a backing file for a loop device, so it's a good indicator of a stuck loop device
  # TODO describe the issue in more detail
  # Using the direct /usr/sbin/losetup as the linstor-satellite image has own wrapper in /usr/local
  stale_loopbacks=$(/usr/sbin/losetup --json | jq -r '.[][] | select(."back-file" == "/" or ."back-file" == "/ (deleted)").name' )
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

  # Detect devices that lost connection and can be simply reconnected
  # This may be fixed in DRBD 9.2.13
  # see https://github.com/LINBIT/drbd/blob/drbd-9.2/ChangeLog
  connecting_devices1="$(get_drbd_connecting)"
  if [ "${connecting_devices1}" != '[]' ]; then

    # wait 10 seconds to avoid false positives
    sleep 1 &
    pid=$!
    wait $pid

    # and check again
    connecting_devices2="$(get_drbd_connecting)"

    export connecting_devices1 connecting_devices2
    stuck_connecting="$(jq -rn '
    env.connecting_devices1 | fromjson as $l1
    | env.connecting_devices2 | fromjson as $l2
    # calculate the intersection
    | $l1 - ($l2 - $l1)
    | .[]
    # output as strings
    | (.name) + " " + (."peer-node-id" | tostring)
    ')"

    while IFS= read -r path; do (
      echo "Trying to reconnect secondary volume ${path}"
      set -x
      # shellcheck disable=SC2086
      drbdsetup disconnect ${path} || echo "Command failed"
      # shellcheck disable=SC2086
      drbdsetup connect ${path} || echo "Command failed"
    ) done <<< "$stuck_connecting"

  fi

done
