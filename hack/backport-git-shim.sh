#!/bin/sh
# A `git` wrapper for .github/workflows/backport.yaml, installed under a name of
# `git` ahead of the real one on PATH for that one job. It adds
# `--allow-empty --empty=drop` to `git cherry-pick` and passes every other
# invocation through untouched.
#
# korthout/backport-action cherry-picks a merged PR's commits one at a time and
# reads a non-zero exit as a conflict: on exit 1 it commits the working tree as
# BACKPORT-CONFLICT and opens a draft PR for manual fixup. An empty cherry-pick
# also exits 1, but leaves nothing to commit, so the action's `git commit` exits
# 1 in turn, it aborts the whole cherry-pick and throws. The commits it had
# already applied are discarded, no branch is pushed and no PR is opened -- the
# original PR gets "Backport failed ... unable to cherry-pick the commit(s)" and
# the fix never reaches the release line. One `git commit --allow-empty` in a PR
# (the usual way to re-trigger CI) is therefore enough to lose the entire
# backport, and so is a commit whose change the target branch already carries.
#
# Neither the pinned action nor upstream v4 accepts cherry-pick flags, and git
# has no config knob for either behaviour, so PATH is the only injection point.
#
# The two flags answer two different cases and neither alone is enough. Measured
# on git 2.55, cherry-picking one commit onto a branch:
#
#   flags                       originally empty  already upstream  conflicting
#   (none)                      exit 1, lost      exit 1, lost      exit 1
#   --allow-empty               carried over      exit 1, lost      exit 1
#   --empty=drop                exit 1, lost      dropped           exit 1
#   --allow-empty --empty=drop  carried over      dropped           exit 1
#
# The last column is what keeps this safe: a real conflict still exits 1 with
# unmerged paths, so the action's draft-PR path -- the behaviour docs/release.md
# describes and hack/release-freeze-contract.bats pins -- is untouched. Only the
# two cases that used to be misreported as conflicts change.
#
# `--empty=drop` needs git 2.45 or newer. The workflow proves the whole wrapper
# against the runner's own git before the action depends on it, because the
# alternative to a red step here is a lost backport reported as a conflict,
# which is the failure this exists to remove.
#
# hack/backport-git-shim.bats covers the behaviour under `make unit-tests`.

set -eu

# Resolving the real git by re-scanning PATH would have to guess which entry is
# this file; the workflow knows, because it captures `command -v git` before
# putting this directory on PATH. Exit 127 rather than 1 on a missing value: the
# action treats 1 as a conflict and would report a misleading one.
real_git=${BACKPORT_REAL_GIT:-}
if [ -z "$real_git" ] || [ ! -x "$real_git" ]; then
  echo "backport git wrapper: set BACKPORT_REAL_GIT to the real git binary" >&2
  exit 127
fi

# The action invokes git as `git <subcommand> [args...]`, so the subcommand is
# always the first argument.
if [ "${1:-}" = "cherry-pick" ]; then
  # `--empty` is mutually exclusive with the sequencer verbs ("fatal:
  # cherry-pick: --empty cannot be used with --abort"), and the action calls
  # `git cherry-pick --abort` on its own error path. Injecting there would turn
  # a reported failure into a wedged sequencer.
  for arg in "$@"; do
    case $arg in
      --abort | --continue | --skip | --quit)
        exec "$real_git" "$@"
        ;;
    esac
  done

  shift
  # The action logs its own invocation without these flags, so without this line
  # a run log gives no way to tell whether the wrapper was in effect.
  echo "backport git wrapper: git cherry-pick --allow-empty --empty=drop $*" >&2
  exec "$real_git" cherry-pick --allow-empty --empty=drop "$@"
fi

exec "$real_git" "$@"
