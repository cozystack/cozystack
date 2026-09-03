#!/usr/bin/env bats

# Behaviour of hack/backport-git-shim.sh, the `git` wrapper .github/workflows/
# backport.yaml installs for its cherry-pick job. The wrapper's whole job is to
# change what `git cherry-pick` returns in two cases and to change nothing else,
# so these tests drive real repositories through the exact sequence
# korthout/backport-action performs rather than grepping the script.
#
# Two of them assert the UNWRAPPED behaviour as well. That is not redundancy:
# the wrapper is only worth its existence while plain git still loses the
# backport, and the day upstream fixes either case is the day this file should
# say so out loud instead of passing for a reason that no longer holds.
#
# The workflow-side wiring -- that backport.yaml installs this file as `git`,
# substitutes the real one into it, and does so between the checkout and the
# action -- is pinned in hack/release-freeze-contract.bats, which already owns
# that job's step contract.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# Isolate from the developer's git configuration entirely. Signing is the one
# that bites: a global commit.gpgsign leaves every commit below waiting on a
# pinentry that never appears.
GIT_CONFIG_GLOBAL=/dev/null
GIT_CONFIG_SYSTEM=/dev/null
GIT_AUTHOR_NAME=cozytest
GIT_AUTHOR_EMAIL=cozytest@invalid
GIT_COMMITTER_NAME=cozytest
GIT_COMMITTER_EMAIL=cozytest@invalid
export GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM
export GIT_AUTHOR_NAME GIT_AUTHOR_EMAIL GIT_COMMITTER_NAME GIT_COMMITTER_EMAIL

REAL_GIT="$(command -v git)"
WORKDIR="$(mktemp -d)"
SHIM_DIR="$WORKDIR/shim"
mkdir -p "$SHIM_DIR"
# Installed exactly as backport.yaml installs it: the committed file is a
# template, and the real git's path is substituted in rather than exported.
sed "s|@REAL_GIT@|$REAL_GIT|" "$REPO_ROOT/hack/backport-git-shim.sh" > "$SHIM_DIR/git"
chmod 0755 "$SHIM_DIR/git"

cozy_cleanup() { rm -rf "$WORKDIR"; }

# Exactly how the action reaches git once the workflow has installed the
# wrapper: subcommand first, wrapper's directory ahead of everything on PATH,
# and nothing else set up for it. Kept to one line so cozytest.sh's translator,
# which appends `return 0` to any line that is exactly `}`, cannot swallow the
# exit status these tests read.
shim_git() { PATH="$SHIM_DIR:$PATH" git "$@"; }

# A repository shaped like the action's input: `target` is the release line, and
# the merged PR's two commits sit ahead of it on `main` -- one real change, then
# an empty commit of the kind used to re-trigger CI. Leaves the caller in the
# new repository with REAL_SHA and EMPTY_SHA set.
#
# Its own exit status is deliberately not load-bearing (see the translator note
# above); every command in it aborts the test under `set -e` if it fails.
setup_repo() {
  rm -rf "$1"
  mkdir -p "$1"
  cd "$1"
  "$REAL_GIT" init -q -b main .
  echo base > f
  "$REAL_GIT" add f
  "$REAL_GIT" commit -q -m "base"
  "$REAL_GIT" branch target
  printf 'base\nfix\n' > f
  "$REAL_GIT" commit -q -am "the real fix"
  REAL_SHA="$("$REAL_GIT" rev-parse HEAD)"
  "$REAL_GIT" commit -q --allow-empty -m "chore: retrigger CI"
  EMPTY_SHA="$("$REAL_GIT" rev-parse HEAD)"
}

# First in the file on purpose. cozytest.sh stops the suite at the first failing
# test, so a git too old to accept the wrapper's flags produces this one message
# instead of three opaque behavioural failures underneath it.
@test "the local git accepts the flags the wrapper injects" {
  setup_repo "$WORKDIR/support"
  "$REAL_GIT" switch -q -c probe target

  rc=0
  "$REAL_GIT" cherry-pick -x --allow-empty --empty=drop "$EMPTY_SHA" >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || {
    echo "this git rejects 'cherry-pick --allow-empty --empty=drop':" >&2
    "$REAL_GIT" --version >&2
    echo "hack/backport-git-shim.sh needs the --empty option, which git gained" >&2
    echo "in 2.45. The backport job runs on a GitHub-hosted ubuntu runner," >&2
    echo "whose git is newer than that; a local git from a distro archive may" >&2
    echo "not be." >&2
    exit 1
  }
}

@test "an empty commit no longer discards the commits already applied" {
  # The regression in one test. The action cherry-picks a merged PR's commits
  # one at a time, and a PR that ends in an empty commit used to lose the
  # branch it had already built -- the real fix included -- because the action
  # reads the empty pick's exit 1 as a conflict and its recovery leaves nothing
  # to commit.
  setup_repo "$WORKDIR/sequence"
  shim_git switch -q -c backport-1-to-target target
  shim_git cherry-pick -x "$REAL_SHA"
  shim_git cherry-pick -x "$EMPTY_SHA"

  # The fix is on the branch, which is the only thing the release line needed.
  grep -qx fix f
  [ "$(shim_git rev-list --count target..HEAD)" -eq 2 ]
}

@test "plain git still loses that backport, which is why the wrapper exists" {
  setup_repo "$WORKDIR/unwrapped"
  "$REAL_GIT" switch -q -c backport-1-to-target target
  "$REAL_GIT" cherry-pick -x "$REAL_SHA"

  rc=0
  "$REAL_GIT" cherry-pick -x "$EMPTY_SHA" >/dev/null 2>&1 || rc=$?
  # Exit 1 is what the action reads as a conflict.
  [ "$rc" -eq 1 ]

  # And this is the recovery it attempts, which fails because an empty pick
  # leaves a clean tree. That second failure is what aborts the cherry-pick and
  # throws away the branch.
  rc=0
  "$REAL_GIT" commit --all -m BACKPORT-CONFLICT >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
}

@test "a commit the target branch already carries is dropped, not reported failed" {
  setup_repo "$WORKDIR/upstream"
  shim_git switch -q -c backport-1-to-target target
  shim_git cherry-pick -x "$REAL_SHA"
  head="$(shim_git rev-parse HEAD)"

  rc=0
  shim_git cherry-pick -x "$REAL_SHA" || rc=$?
  [ "$rc" -eq 0 ]
  # Dropped rather than kept as an empty commit: `--empty=drop`, not
  # `--empty=keep`. A backport PR should not carry a commit that changes
  # nothing.
  [ "$(shim_git rev-parse HEAD)" = "$head" ]
}

@test "a real conflict still exits 1 with unmerged paths" {
  # The load-bearing half of the change. Everything the workflow documents
  # about conflicts -- the BACKPORT-CONFLICT commit, the draft PR, the fixup
  # instructions -- hangs off this exit status, so the wrapper must not have
  # made conflicts look like successes.
  setup_repo "$WORKDIR/conflict"
  shim_git switch -q -c backport-1-to-target target
  printf 'base\ndiverged\n' > f
  shim_git commit -q -am "a divergent change on the release line"

  rc=0
  shim_git cherry-pick -x "$REAL_SHA" || rc=$?
  [ "$rc" -eq 1 ]
  [ -n "$(shim_git diff --name-only --diff-filter=U)" ]

  # And the action's recovery from here still works, which is what turns this
  # into a draft PR instead of a lost backport.
  shim_git commit --all -m BACKPORT-CONFLICT
  [ "$(shim_git log -1 --pretty=%s)" = "BACKPORT-CONFLICT" ]
}

@test "the sequencer verbs pass through, because --empty cannot join them" {
  setup_repo "$WORKDIR/abort"
  shim_git switch -q -c backport-1-to-target target
  printf 'base\ndiverged\n' > f
  shim_git commit -q -am "a divergent change on the release line"
  head="$(shim_git rev-parse HEAD)"

  rc=0
  shim_git cherry-pick -x "$REAL_SHA" || rc=$?
  [ "$rc" -eq 1 ]

  # The action calls this on its own error path. Injecting the flags here would
  # replace a reported failure with a wedged sequencer.
  shim_git cherry-pick --abort
  [ "$(shim_git rev-parse HEAD)" = "$head" ]
  [ -z "$(shim_git status --porcelain)" ]

  # Why that passthrough is load-bearing rather than tidiness: git refuses the
  # combination outright, and refuses it before it looks at sequencer state.
  rc=0
  "$REAL_GIT" cherry-pick --abort --empty=drop >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
}

@test "every other subcommand reaches the real git unchanged" {
  setup_repo "$WORKDIR/passthrough"

  [ "$(shim_git rev-parse --abbrev-ref HEAD)" = "main" ]
  [ "$(shim_git rev-list --count HEAD)" -eq 3 ]
  [ "$(shim_git log -1 --pretty=%s)" = "chore: retrigger CI" ]
}

@test "a wrapper that cannot find the real git fails distinguishably from a conflict" {
  # Exit 1 is the action's "conflict" signal, and a misconfigured wrapper
  # answering 1 would be reported on the PR as a merge conflict that does not
  # exist. Both broken installs must answer something else.
  setup_repo "$WORKDIR/misconfigured"

  # The committed template, copied without the substitution the install step
  # performs -- the mistake a second caller of this script would make.
  broken="$WORKDIR/broken"
  mkdir -p "$broken"
  cp "$REPO_ROOT/hack/backport-git-shim.sh" "$broken/git"
  chmod 0755 "$broken/git"
  rc=0
  PATH="$broken:$PATH" git rev-parse HEAD >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 127 ]

  # And a path substituted in that is not there any more.
  absent="$WORKDIR/absent"
  mkdir -p "$absent"
  sed "s|@REAL_GIT@|$WORKDIR/no-such-git|" "$REPO_ROOT/hack/backport-git-shim.sh" > "$absent/git"
  chmod 0755 "$absent/git"
  rc=0
  PATH="$absent:$PATH" git rev-parse HEAD >/dev/null 2>&1 || rc=$?
  [ "$rc" -eq 127 ]
}
