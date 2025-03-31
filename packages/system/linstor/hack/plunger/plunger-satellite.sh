#!/bin/bash
set -e

terminate() {
  echo "Caught signal, terminating"
  exit 0
}

trap terminate SIGINT SIGQUIT SIGTERM

echo "Running Linstor per-satellite plunger:"
cat "${0}"

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
    /usr/sbin/losetup --detach "${stale_device}"
  ); done

  # Detect secondary volumes that lost connection and can be simply reconnected
  disconnected_secondaries=$(drbdadm status 2>/dev/null | awk '/pvc-.*role:Secondary.*force-io-failures:yes/ {print $1}')
  for secondary in $disconnected_secondaries; do (
    echo "Trying to reconnect secondary volume ${secondary}"
    set -x
    drbdadm down "${secondary}"
    drbdadm up "${secondary}"
  ); done

done
