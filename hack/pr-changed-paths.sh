#!/bin/sh
# Write the list of paths a pull request changes, as its build decisions read
# it. Every package the pull request does not build is filled from the
# packages artifact hack/pr-artifact-ref.sh picks for its base. Main and the
# release lines read their own, so the pull request's own diff is the whole
# question there. A pull request stacked on another one reads the artifact of
# main or a release line, which has none of the stack's changes, so the list
# is taken against the fork point from that artifact's line and the images of
# the lower pull requests are built too. A fork point that cannot be found
# fails, and the caller builds everything.
#
# Usage: pr-changed-paths.sh [--renames] <base-ref> <output-file>
#   The list is rename-blind, both sides of a rename, unless --renames asks for
#   git's rename detection.
set -eu

renames=--no-renames
if [ "${1:-}" = --renames ]; then
  renames=
  shift
fi
if [ "$#" -ne 2 ]; then
  echo "usage: pr-changed-paths.sh [--renames] <base-ref> <output-file>" >&2
  exit 2
fi
base=$1
out=$2

artifact="$("$(dirname "$0")/pr-artifact-ref.sh" "$base")"
if [ "$artifact" = "$base" ]; then
  range="origin/${base}...HEAD"
else
  range="$(git merge-base "origin/${artifact}" HEAD)..HEAD"
fi
# shellcheck disable=SC2086 # an empty $renames is no argument
git diff --name-only $renames "$range" > "$out"
