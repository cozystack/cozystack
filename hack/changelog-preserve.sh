#!/bin/sh
# Usage: changelog-preserve.sh <staging-branch> <version> <outfile>
#
# Rescue an existing changelog from a staging branch that is about to be rebuilt.
#
# promote-rc.yaml reconstructs release-X.Y.Z from the rc tree on every dispatch
# and force-pushes it. Anything committed onto that branch in the meantime is
# discarded — including the two things most worth keeping: a changelog a
# maintainer wrote by hand (the documented recovery when generation fails) and an
# earlier dispatch's reviewed output. Losing either to a retry silently replaces
# human-edited release notes with a fresh AI run, or with nothing at all.
#
# Run BEFORE `git checkout -B <staging-branch>`, from inside the repo, with
# `origin` configured. On success the changelog is written to <outfile> for the
# caller to restore after the reset.
#
# Best-effort by contract: a missing branch, a branch without a changelog, and an
# unreachable remote are all "nothing to preserve" (exit 1), never a hard error.
# The caller is mid-promotion and must not be wedged by this.
#
# Exit 0 = preserved, <outfile> written. Exit 1 = nothing to preserve.

set -eu

BRANCH="${1:?usage: changelog-preserve.sh <staging-branch> <version> <outfile>}"
VERSION="${2:?usage: changelog-preserve.sh <staging-branch> <version> <outfile>}"
OUTFILE="${3:?usage: changelog-preserve.sh <staging-branch> <version> <outfile>}"

CL="docs/changelogs/v${VERSION}.md"

# `git fetch <branch>` fails when the branch does not exist on the remote, which
# is the ordinary first-promotion case — not an error worth reporting loudly.
if ! git fetch -q origin "$BRANCH" 2>/dev/null; then
  echo "No remote branch ${BRANCH}; nothing to preserve." >&2
  exit 1
fi

if ! git cat-file -e "FETCH_HEAD:${CL}" 2>/dev/null; then
  echo "Branch ${BRANCH} carries no ${CL}; nothing to preserve." >&2
  exit 1
fi

# An empty file on the branch is not worth carrying over: it would suppress
# generation (the caller treats a present file as authoritative) while being
# useless as release notes. Let it be rebuilt instead.
if [ ! -s "$OUTFILE" ] && ! git show "FETCH_HEAD:${CL}" > "$OUTFILE" 2>/dev/null; then
  echo "Could not read ${CL} from ${BRANCH}; nothing to preserve." >&2
  rm -f "$OUTFILE"
  exit 1
fi

if [ ! -s "$OUTFILE" ]; then
  echo "${CL} on ${BRANCH} is empty; nothing to preserve." >&2
  rm -f "$OUTFILE"
  exit 1
fi

echo "Preserved ${CL} from ${BRANCH} ($(wc -l < "$OUTFILE" | tr -d ' ') lines)." >&2
exit 0
