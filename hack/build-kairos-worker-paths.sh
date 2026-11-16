#!/bin/sh
# Write the rename-blind list of paths that decides whether a pull request
# builds the Kairos worker disks. A pull request that does not build them takes
# the disk refs from a packages artifact: its base's own on main and the
# release lines, and main's or the nearest release line's on any other base.
# On main and the release lines the pull request's own diff is the whole
# question. A pull request stacked on another one can carry disk-input changes
# of its parent that are in no artifact, and its own diff against the parent
# does not show them, so the list is taken against its fork point from main.
# A fork point that cannot be found fails, and the caller builds.
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: build-kairos-worker-paths.sh <base-ref> <output-file>" >&2
  exit 2
fi
base=$1
out=$2

# The same test the overlay step in pull-requests.yaml applies to decide which
# bases publish an artifact of their own.
if printf '%s\n' "$base" | grep -qE '^(main|release-[0-9]+\.[0-9]+)$'; then
  range="origin/${base}...HEAD"
else
  range="$(git merge-base origin/main HEAD)..HEAD"
fi
git diff --name-only --no-renames "$range" > "$out"
