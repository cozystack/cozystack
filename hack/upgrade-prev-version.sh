#!/bin/sh
# -----------------------------------------------------------------------------
# upgrade-prev-version.sh — resolve the baseline ("upgrade FROM") version for
# the upgrade E2E lane: the latest STABLE release on the minor line immediately
# below the target version. Implements the "previous latest minor release ->
# current version" path the upgrade test exercises (cozystack/cozystack#2401).
#
# Usage:
#   upgrade-prev-version.sh [TARGET] [TAGS_FILE]
#
#   TARGET     version being upgraded TO (vX.Y.Z[-rc.N], leading v optional).
#              Falls back to $UPGRADE_TARGET_TAG. When neither is set (a normal
#              PR that carries no tag) the baseline is the highest stable tag
#              overall — i.e. "upgrade from the latest release to this build".
#   TAGS_FILE  optional newline-separated tag list used INSTEAD of `git tag`.
#              Test hook, mirroring select-e2e.sh's sources-dir override so the
#              resolver is unit-testable against synthetic tag sets without a
#              real git history (see hack/upgrade-prev-version_test.bats).
#
# Prints the resolved tag (e.g. v1.5.3) to stdout. Errors to stderr, exit 1.
# Runs under /bin/sh (dash on Ubuntu CI) — no bashisms.
# -----------------------------------------------------------------------------
set -eu

target="${1:-${UPGRADE_TARGET_TAG:-}}"
TAGS_FILE="${2:-}"

all_tags() {
  if [ -n "$TAGS_FILE" ]; then cat "$TAGS_FILE"; else git tag -l 'v*'; fi
}

# Stable (non-prerelease) vX.Y.Z tags only, highest version first. Any tag with
# a hyphen is a SemVer pre-release (v1.6.0-rc.1) and is excluded by the regex.
stable_desc() {
  all_tags | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -rV
}

# Latest stable tag on the given "MAJOR.MINOR" line (empty if none). Dots in the
# line are escaped so line "1.5" cannot spuriously match "v155.x".
latest_on_line() {
  _line=$(printf '%s' "$1" | sed 's/\./\\./g')
  stable_desc | grep -E "^v${_line}\.[0-9]+$" | head -n1
}

# No explicit target: baseline is the highest stable tag overall.
if [ -z "$target" ]; then
  prev=$(stable_desc | head -n1)
  [ -n "$prev" ] || { echo "ERROR: no stable release tags found" >&2; exit 1; }
  printf '%s\n' "$prev"
  exit 0
fi

ver="${target#v}"
ver="${ver%%-*}"                       # strip the -rc.N / -beta.N suffix
maj=$(printf '%s' "$ver" | cut -d. -f1)
min=$(printf '%s' "$ver" | cut -d. -f2)
case "${maj}:${min}" in
  *[!0-9:]*|:*|*:) echo "ERROR: cannot parse target version '$target'" >&2; exit 1 ;;
esac

# Walk minor lines downward from (min-1) so a target whose immediate predecessor
# line never shipped a stable release still resolves to a real baseline.
i=$((min - 1))
while [ "$i" -ge 0 ]; do
  prev=$(latest_on_line "${maj}.${i}")
  [ -n "$prev" ] && { printf '%s\n' "$prev"; exit 0; }
  i=$((i - 1))
done

# Nothing below on this major (e.g. target is vX.0.z): fall back to the highest
# stable of the previous major line.
if [ "$maj" -gt 0 ]; then
  prev=$(stable_desc | grep -E "^v$((maj - 1))\." | head -n1)
  [ -n "$prev" ] && { printf '%s\n' "$prev"; exit 0; }
fi

echo "ERROR: no previous stable minor release found below '$target'" >&2
exit 1
