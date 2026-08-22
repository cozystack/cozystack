#!/bin/sh
# Usage: select-changelog-source.sh <version> <rc_branch>
#
# Decide which pre-existing changelog (if any) release generation should use,
# WITHOUT running the AI — validate-then-fall-through across the sources, so an
# invalid higher-priority file can never silently suppress a valid lower-priority
# one:
#
#   1. docs/changelogs/vX.Y.Z.md already in the working tree (the rc TAG tree) —
#      a maintainer hand-wrote it before the tag was cut.
#   2. docs/changelogs/vX.Y.Z.md on the rc STAGING branch <rc_branch> —
#      changelog-rc.yaml committed it there at rc time (the "copy" pickup).
#
# Each candidate is validated with validate-changelog.sh; an invalid one is
# reported with a ::warning:: and DISCARDED (rm) so the next source is tried.
# Exactly one machine-readable line is printed as the LAST line of stdout:
#
#   source=rc-tag | source=rc-branch | source=none
#
# On rc-tag / rc-branch the usable changelog is left at docs/changelogs/vX.Y.Z.md.
# On none nothing usable was found and the caller must generate. The ::warning::
# annotations for discarded files are printed to stdout too, ahead of that line.
#
# Run from the repo root AFTER the .release-tooling overlay, with `origin`
# fetch-authenticated (changelog-preserve.sh fetches the branch).
# validate-changelog.sh and changelog-preserve.sh must sit beside this script.
#
# Exit 0 = a decision was reached (source line printed). Exit 1 = bad args.

set -eu

VERSION="${1:?usage: select-changelog-source.sh <version> <rc_branch>}"
RC_BRANCH="${2:?usage: select-changelog-source.sh <version> <rc_branch>}"

HERE="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
VALIDATE="${HERE}/validate-changelog.sh"
PRESERVE="${HERE}/changelog-preserve.sh"
CL="docs/changelogs/v${VERSION}.md"

# 1. The rc tag tree — validate before trusting it.
if [ -s "$CL" ]; then
  if REASON="$("$VALIDATE" "$CL" "$VERSION" 2>&1)"; then
    echo "::notice::Using ${CL} already in the rc tag tree (${REASON})."
    echo "source=rc-tag"
    exit 0
  fi
  echo "::warning::${CL} is in the rc tag tree but is NOT usable (${REASON}); discarding it and trying the rc staging branch ${RC_BRANCH}."
  rm -f "$CL"
fi

# 2. The rc staging branch — fetch, then validate before trusting it.
mkdir -p docs/changelogs
if "$PRESERVE" "$RC_BRANCH" "$VERSION" "$CL"; then
  if REASON="$("$VALIDATE" "$CL" "$VERSION" 2>&1)"; then
    echo "::notice::Copied ${CL} from rc staging branch ${RC_BRANCH} (${REASON})."
    echo "source=rc-branch"
    exit 0
  fi
  echo "::warning::${CL} on rc staging branch ${RC_BRANCH} is NOT usable (${REASON}); discarding it and generating."
  rm -f "$CL"
fi

echo "source=none"
exit 0
