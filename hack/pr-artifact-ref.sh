#!/bin/sh
# Print the packages artifact a pull request on <base-ref> reads its unbuilt
# images from. Main and the release lines publish their own. Any other base,
# a stacked pull request's feature branch, reads the artifact of whichever of
# main and the lines it has the fewest commits on top of; a tie goes to main.
# A line with no commit of its own yet is main at the cut commit in the graph,
# so a stack on it ties and reads main's, and keeps doing so until it contains
# a commit of the line's own. Breaking the tie towards the line instead would
# misroute the far more common branch forked from main before the cut, which
# ties the same way. A base deleted since the run started reads main's.
set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: pr-artifact-ref.sh <base-ref>" >&2
  exit 2
fi
base=$1

if [ "$base" = main ] || printf '%s\n' "$base" | grep -qE '^release-[0-9]+\.[0-9]+$'; then
  printf '%s\n' "$base"
  exit 0
fi
artifact=main
if git rev-parse --verify --quiet "origin/${base}" >/dev/null; then
  best="$(git rev-list --count "origin/main..origin/${base}")"
  for line in $(git for-each-ref --format='%(refname:lstrip=3)' 'refs/remotes/origin/release-*' \
                  | grep -E '^release-[0-9]+\.[0-9]+$'); do
    n="$(git rev-list --count "origin/${line}..origin/${base}")"
    if [ "$n" -lt "$best" ]; then best="$n"; artifact="$line"; fi
  done
fi
printf '%s\n' "$artifact"
