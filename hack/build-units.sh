#!/usr/bin/env bash
# Builds the image units hack/build-matrix.sh lists, BUILD_JOBS at a time.
# Several build at once, so every output line starts with its unit.
#
# Run from the repository root. Talos and the installer are not in the list;
# `make build-parallel` builds them afterwards.
set -euo pipefail

jobs="${BUILD_JOBS:-4}"

# It rewrites packages/system/seaweedfs/values.yaml, which the seaweedfs build
# rewrites too, so it waits until the others are done.
last=packages/system/objectstorage-controller

units="$(hack/build-matrix.sh | jq --raw-output '.[]')"
[ -n "$units" ]

build() {
  set -o pipefail
  if make -C "$1" image 2>&1 | sed --unbuffered "s|^|$1: |"; then
    echo "built $1"
  else
    echo "::error::$1 failed"
    return 1
  fi
}
export -f build

grep --invert-match --line-regexp --fixed-strings "$last" <<< "$units" |
  xargs --max-procs="$jobs" --max-args=1 bash -c 'build "$1"' _

if grep --quiet --line-regexp --fixed-strings "$last" <<< "$units"; then
  build "$last"
fi
