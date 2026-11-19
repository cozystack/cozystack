#!/bin/sh
set -eu

SELF_PID="$$"
INTERVAL=30m

while [ $# -gt 0 ]; do
  case "$1" in
    --timeout)
      INTERVAL="$2"
      shift 2
      ;;
    *)
      echo "Usage: $0 [--timeout SECONDS]"
      exit 1
      ;;
  esac
done

check_once() {
  ALL_PROCS=$(ps -eo pid=,comm=)
  OWN_PIDS=$(pstree -p $$ | grep -o '[0-9]\+' | sort -u)

  EXTERNAL_PIDS=$(
    echo "$ALL_PROCS" | while read -r PID CMD; do
      PID=$(echo "$PID" | tr -d ' ')
      CMD=$(echo "$CMD" | tr -d ' ')

      echo "$OWN_PIDS" | grep -q -x "$PID" && continue

      case "$CMD" in
        *qemu*|ps) continue ;;
      esac

      echo "PID=$PID CMD=$CMD"
    done
  )

  COUNT=$(echo "$EXTERNAL_PIDS" | wc -w)
  echo "$EXTERNAL_PIDS"
  [ "$COUNT" -eq 0 ]
}

check_loop() {
  while :; do
    ALL_PROCS=$(ps -eo pid=,ppid=,comm=)

    if check_once; then
      echo "No external processes, exiting..."
      exit 0
    fi

    echo "External processes still running, next check in ${INTERVAL}..."
    sleep "$INTERVAL"
  done
}

echo "Waiting for external processes to be started, next check in ${INTERVAL}..."
sleep "$INTERVAL"
check_loop
