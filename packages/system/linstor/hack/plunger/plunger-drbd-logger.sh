#!/bin/bash
set -e

terminate() {
  echo "Caught signal, terminating"
  exit 0
}

trap terminate SIGINT SIGQUIT SIGTERM

echo "Started logger of bad DRBD statuses"

while true; do

  all_devices="$(drbdsetup status --json 2>/dev/null)"
  unhealthy_devices="$(echo "${all_devices}" | jq -r '
  map(select(
    .suspended != false or
    ."force-io-failures" != false or
    # Diskless can be legit when allowRemoteVolumeAccess is set to "true"
    # TODO how does forced-diskless look?
    ([.devices[]."disk-state"] | inside(["UpToDate", "Consistent", "Diskless"]) | not) or
    (.connections[]."connection-state" != "Connected") or
    # congested is not an alarm but an indicator
    (.connections[]."congested" != false) or
    (.connections[].peer_devices[]."replication-state" != "Established")
  ))
  | unique
  ')"
  if [ "${unhealthy_devices}" != '[]' ]; then
    echo -e "Unhealthy devices:\n${unhealthy_devices}"
  fi

  sleep 30 &
  pid=$!
  wait $pid

done
