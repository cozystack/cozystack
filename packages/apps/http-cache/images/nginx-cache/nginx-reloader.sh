#!/bin/sh
set -e

cleanup() {
  echo "Received termination signal. Exiting..."
  exit 0
}

trap cleanup INT
while true; do
  inotifywait -s -e close_write,attrib --include 'reload' /data >/dev/null
  nginx -s reload
done
