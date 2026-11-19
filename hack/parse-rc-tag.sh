#!/bin/sh
# Usage: parse-rc-tag.sh <rc_tag>
#
# Strictly parse a release-candidate tag and print the derived release
# identifiers as `key=value` lines, ready to append to "$GITHUB_OUTPUT":
#
#   rc_tag=v1.6.0-rc.2
#   stable_version=1.6.0
#   rc_branch=release-1.6.0-rc.2
#
# Whitespace is REJECTED, never stripped. Stripping (`tr -d '[:space:]'`) would
# silently "correct" a typo like 'v1. 6.0-rc.2' into a different, valid-looking
# tag. Worse, a caller that keys its concurrency lane on the raw dispatch input
# would then run two spellings of the same tag ('v1.6.0-rc.2' and
# 'v1.6.0-rc.2 ') in different lanes, racing to push the same staging branch.
# Rejecting outright makes the raw input a caller saw byte-identical to the tag
# acted on here: one canonical spelling per tag, or a hard error.
#
# Exit 0 = valid; the three key=value lines on stdout, nothing on stderr.
# Exit 1 = invalid; the reason on stderr, nothing on stdout.

set -eu

RC_TAG="${1?usage: parse-rc-tag.sh <rc_tag>}"

fail() {
  echo "$1" >&2
  exit 1
}

# Reject any whitespace (leading, trailing, or internal) and the empty string
# outright, before the pattern match — a stripped-then-matched value would let a
# whitespace-bearing spelling through as if it were the canonical tag.
case "$RC_TAG" in
  '')            fail "rc_tag is empty" ;;
  *[[:space:]]*) fail "rc_tag '$RC_TAG' must not contain whitespace" ;;
esac

printf '%s' "$RC_TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' \
  || fail "rc_tag '$RC_TAG' must match 'vX.Y.Z-rc.N' (e.g. v1.6.0-rc.1)"

rest="${RC_TAG#v}"       # 1.6.0-rc.2
version="${rest%-rc.*}"  # 1.6.0
n="${rest##*-rc.}"       # 2

printf 'rc_tag=v%s-rc.%s\n' "$version" "$n"
printf 'stable_version=%s\n' "$version"
printf 'rc_branch=release-%s-rc.%s\n' "$version" "$n"
