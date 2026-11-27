#!/usr/bin/env bats
# Unit tests for hack/pr-changed-paths.sh, the changed-path list a pull request's
# build decisions read, run against small git fixtures.
#
# Run with: bats hack/pr-changed-paths_test.bats

load test_helper

setup() {
  strict_setup
  ROOT="$PWD"
  repo=$(mktemp -d)
  git -C "$repo" init -q -b main
  git -C "$repo" config commit.gpgsign false
  mkdir -p "$repo/packages/system/controller" "$repo/hack"
  printf 'a\n' > "$repo/packages/system/controller/main.go"
  printf 'a\n' > "$repo/hack/x_test.bats"
  commit "$repo" main
  git -C "$repo" update-ref refs/remotes/origin/main HEAD
}

teardown() {
  rm -rf "$repo"
}

commit() {
  git -C "$1" add -A
  git -C "$1" -c user.name=T -c user.email=t@example.org commit -qm "$2"
}

paths() {
  (cd "$repo" && "$ROOT/hack/pr-changed-paths.sh" "$@" "$repo/out.txt") || return
  cat "$repo/out.txt"
}

# The base gains a commit after the pull request forked from it; the
# pull request's list must not include it.
advance() {
  git -C "$repo" checkout -q "$1"
  mkdir -p "$repo/docs"
  printf 'x\n' > "$repo/docs/later.md"
  commit "$repo" "$1 later"
  git -C "$repo" update-ref "refs/remotes/origin/$1" HEAD
  git -C "$repo" checkout -q pr
}

# A stacked pull request: the parent changes the controller, the child only a test.
stack() {
  git -C "$repo" checkout -q -b parent
  printf 'b\n' > "$repo/packages/system/controller/main.go"
  commit "$repo" parent
  git -C "$repo" update-ref refs/remotes/origin/parent HEAD
  git -C "$repo" checkout -q -b child
  printf 'b\n' > "$repo/hack/x_test.bats"
  commit "$repo" child
}

@test "a pull request on main lists its own changes" {
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/hack/x_test.bats"
  commit "$repo" pr
  advance main
  [ "$(paths main)" = hack/x_test.bats ]
}

@test "a pull request on a release line lists only what it changed on that line" {
  git -C "$repo" checkout -q -b release-1.6
  printf 'b\n' > "$repo/packages/system/controller/main.go"
  commit "$repo" line
  git -C "$repo" update-ref refs/remotes/origin/release-1.6 HEAD
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/hack/x_test.bats"
  commit "$repo" pr
  advance release-1.6
  [ "$(paths release-1.6)" = hack/x_test.bats ]
}

@test "a stacked pull request lists the stack's changes against main as well as its own" {
  stack
  [ "$(paths parent)" = "$(printf 'hack/x_test.bats\npackages/system/controller/main.go')" ]
}

@test "a branch only named like a release line counts as stacked" {
  git -C "$repo" checkout -q -b release-1.8.1
  printf 'b\n' > "$repo/packages/system/controller/main.go"
  commit "$repo" parent
  git -C "$repo" update-ref refs/remotes/origin/release-1.8.1 HEAD
  git -C "$repo" checkout -q -b pr
  printf 'b\n' > "$repo/hack/x_test.bats"
  commit "$repo" pr
  [ "$(paths release-1.8.1)" = "$(printf 'hack/x_test.bats\npackages/system/controller/main.go')" ]
}

@test "a stacked pull request whose fork point from main is unknown fails" {
  stack
  git -C "$repo" update-ref -d refs/remotes/origin/main
  if paths parent 2>/dev/null; then
    echo "listed paths without a fork point from main" >&2
    false
  fi
}

@test "the rename-blind list keeps both sides of a rename, the other only the new path" {
  git -C "$repo" checkout -q -b pr
  git -C "$repo" mv packages/system/controller/main.go hack/main.go
  commit "$repo" rename
  [ "$(paths main)" = "$(printf 'hack/main.go\npackages/system/controller/main.go')" ]
  [ "$(paths --renames main)" = hack/main.go ]
}

@test "a wrong argument count is a usage error" {
  rc=0
  (cd "$repo" && "$ROOT/hack/pr-changed-paths.sh" main 2>/dev/null) || rc=$?
  [ "$rc" -eq 2 ]
}

@test "a pull request stacked on a release line lists changes against that line, not against main" {
  git -C "$repo" checkout -q -b release-1.6
  printf 'b\n' > "$repo/packages/system/controller/main.go"
  commit "$repo" line
  git -C "$repo" update-ref refs/remotes/origin/release-1.6 HEAD
  git -C "$repo" checkout -q -b parent
  mkdir -p "$repo/packages/apps/db"
  printf 'a\n' > "$repo/packages/apps/db/values.yaml"
  commit "$repo" parent
  git -C "$repo" update-ref refs/remotes/origin/parent HEAD
  git -C "$repo" checkout -q -b child
  printf 'b\n' > "$repo/hack/x_test.bats"
  commit "$repo" child
  [ "$(paths parent)" = "$(printf 'hack/x_test.bats\npackages/apps/db/values.yaml')" ]
}

# The plan step must not trust any list when the script fails: it builds every
# image and Talos, runs every gated test and keeps every package's refs.
@test "the plan step trusts none of the lists when they cannot be computed" {
  sandbox=$(mktemp -d)
  mkdir "$sandbox/hack"
  for f in "$ROOT"/hack/*; do ln -s "$f" "$sandbox/hack/"; done
  rm "$sandbox/hack/pr-changed-paths.sh"
  printf '#!/bin/sh\nexit 1\n' > "$sandbox/hack/pr-changed-paths.sh"
  chmod +x "$sandbox/hack/pr-changed-paths.sh"
  ln -s "$ROOT/Makefile" "$sandbox/Makefile"
  (cd "$ROOT" && find packages -mindepth 1 -maxdepth 2 -type d) | (cd "$sandbox" && xargs mkdir -p)
  # The step's scratch files move into the sandbox, so nothing left in /tmp
  # by another run can stand in for a list the step failed to write.
  yq '.jobs[].steps[]? | select(.id == "p") | .run' "$ROOT/.github/workflows/pull-requests.yaml" \
    | sed "s#/tmp/#$sandbox/#g" > "$sandbox/step.sh"
  rc=0
  (cd "$sandbox" && GITHUB_OUTPUT="$sandbox/out" BASE_REF=parent bash -eo pipefail step.sh > "$sandbox/log" 2>&1) || rc=$?
  [ "$rc" -eq 0 ] || { cat "$sandbox/log" >&2; false; }
  out=$(cat "$sandbox/out")
  full=$(cd "$sandbox" && hack/build-matrix.sh FULL)
  every=$(cd "$sandbox" && find packages -mindepth 2 -maxdepth 2 -type d | sort | tr '\n' ' ')
  rm -rf "$sandbox"
  printf '%s\n' "$out" | grep -qx 'code=true'
  printf '%s\n' "$out" | grep -qx 'talos=true'
  printf '%s\n' "$out" | grep -qx 'backport_batch=true'
  printf '%s\n' "$out" | grep -qxF "matrix=$full"
  [ -n "$every" ]
  printf '%s\n' "$out" | grep -qxF "touched=$every"
  printf '%s\n' "$out" | grep -qx 'any=true'
}
