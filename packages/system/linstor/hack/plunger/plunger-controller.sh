#!/bin/bash
set -e

terminate() {
  echo "Caught signal, terminating"
  exit 0
}

trap terminate SIGINT SIGQUIT SIGTERM

echo "Running Linstor controller plunger:"
cat "${0}"

while true; do
  # timeout at the start of the loop to give some time for the linstor-controller to start
  sleep 30 &
  pid=$!
  wait $pid

  # workaround for https://github.com/LINBIT/linstor-server/issues/437
  # try to delete snapshots that are stuck in the DELETE state
  linstor -m s l \
  | jq -r '.[][] | select(.flags | contains(["DELETE"])) | "linstor snapshot delete \(.resource_name) \(.name)"' \
  | sh -x
done
