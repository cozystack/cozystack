#!/bin/sh
# Usage: hack/validate-changelog.sh <changelog-file> <version-without-v>
#
# Decides whether a changelog file is publishable as a GitHub Release body.
#
# promote-rc.yaml lets the AI generation step fail (a Copilot outage must never
# block a release), which means a run can die mid-stream and leave a fragment
# behind. Without this check that fragment would be committed, advertised as a
# valid changelog in the promote PR body, and published verbatim by finalize as
# the release notes — worse than having no changelog, because nobody is told.
#
# The assertions are deliberately narrow: only invariants every shipped changelog
# in docs/changelogs/ actually satisfies. Two forms exist and both are valid —
# minor releases use `# Cozystack vX.Y.Z`, patch releases use `# vX.Y.Z (date)` —
# so the header check accepts either. There is deliberately NO line-count floor:
# v1.5.1 is a complete, shipped, 19-line patch changelog, and a floor set above
# it would reject good output and mislabel it as a generation failure.
#
# The version is checked into the header, the release-link comment and the
# compare link, so a changelog generated for the wrong version (an rc string, or
# a stale version carried over from a retry) is caught rather than published.
#
# Exit 0 = publishable. Exit 1 = not publishable, with the reason on stderr.

set -eu

CL="${1:?usage: validate-changelog.sh <file> <version>}"
VERSION="${2:?usage: validate-changelog.sh <file> <version>}"

fail() {
  echo "$1" >&2
  exit 1
}

[ -f "$CL" ] || fail "file absent: $CL"
[ -s "$CL" ] || fail "file empty: $CL"
grep -q '[^[:space:]]' "$CL" || fail "file is only whitespace: $CL"

# Both header conventions. \b is not portable in POSIX grep -E, so anchor on
# "end of line or a non-version character" instead: that keeps v1.5.1 from
# satisfying a check for v1.5.11.
grep -qE "^# (Cozystack )?v${VERSION}([^0-9.]|\$)" "$CL" \
  || fail "missing an H1 for v${VERSION}: expected '# Cozystack v${VERSION}' (minor) or '# v${VERSION} (<date>)' (patch)"

# Leading HTML comment pointing at the release. Present in every shipped
# changelog and the first thing a truncated write would still have, so on its
# own it is weak — it is the version check here that earns its keep.
grep -qF "releases/tag/v${VERSION}" "$CL" \
  || fail "missing the release-link comment for v${VERSION}"

# A truncated stream loses the tail, so assert something that only exists at the
# end. The compare link must also name this version, catching a changelog
# generated against the rc tag or a stale version.
grep -qE "compare/.*\.\.\.v${VERSION}([^0-9.]|\$)" "$CL" \
  || fail "missing the 'Full Changelog' compare link ending in v${VERSION} (likely truncated, or generated for the wrong version)"

# At least one section. Distinguishes a real changelog from a header plus a
# stub line, without assuming which sections a given release happens to have.
grep -qE '^## ' "$CL" \
  || fail "no '## ' sections — not a complete changelog"

echo "OK: $CL is publishable as the v${VERSION} release body ($(wc -l < "$CL" | tr -d ' ') lines)"
