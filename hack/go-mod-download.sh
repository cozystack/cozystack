#!/bin/sh
# `go mod download` with a bounded retry. proxy.golang.org now and then resets
# an HTTP/2 stream mid-download ("stream error ... INTERNAL_ERROR"), and one
# reset otherwise fails the whole job. A retry is safe: the module cache keeps
# whatever finished, and a module whose checksum does not match go.sum fails
# every attempt the same way.
#
# Runner jobs call it; Dockerfiles carry the same loop inline, as
# hack/go-module-fetch-contract.bats explains.
set -eu

attempts=3
backoff="${GO_MOD_DOWNLOAD_BACKOFF:-10}"
case "$backoff" in
  '' | *[!0-9]*)
    echo "go mod download: GO_MOD_DOWNLOAD_BACKOFF must be a whole number of seconds, got '$backoff'" >&2
    exit 2
    ;;
esac
attempt=1
until go mod download "$@"; do
  if [ "$attempt" -ge "$attempts" ]; then
    echo "go mod download: giving up after $attempt attempts" >&2
    exit 1
  fi
  echo "go mod download: attempt $attempt of $attempts failed, retrying in $((attempt * backoff))s" >&2
  sleep $((attempt * backoff))
  attempt=$((attempt + 1))
done
