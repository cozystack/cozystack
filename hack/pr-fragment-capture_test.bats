#!/usr/bin/env bats
# The matrix build hands its stamped refs to finalize as a patch fragment. A
# package's image target may create a ref-bearing file rather than edit one, so
# the fragment has to carry new files as well as changed ones.

load test_helper

ROOT="$(pwd)"
WF="$ROOT/.github/workflows/pull-requests.yaml"

@test "the matrix fragment carries files the package build created" {
  dir="$(mktemp -d)"
  repo="$dir/repo"
  pkg=packages/apps/foo
  mkdir -p "$repo/$pkg/images" "$repo/packages/apps/bar"
  git -C "$repo" init -q
  git -C "$repo" config user.email ci@example.invalid
  git -C "$repo" config user.name CI
  git -C "$repo" config commit.gpgsign false
  git -C "$repo" config core.hooksPath /dev/null
  printf 'registry.example/foo:old@sha256:old\n' > "$repo/$pkg/images/foo.tag"
  git -C "$repo" add .
  git -C "$repo" commit -q -m seed
  printf 'registry.example/foo:pr@sha256:changed\n' > "$repo/$pkg/images/foo.tag"
  printf 'registry.example/new:pr@sha256:created\n' > "$repo/$pkg/images/new.tag"
  printf 'stray\n' > "$repo/packages/apps/bar/stray.txt"

  step="$(yq '.jobs.build.steps[] | select(.name == "Capture digest patch fragment").run' "$WF")"
  [ -n "$step" ]
  (cd "$repo" && PKG="$pkg" GITHUB_OUTPUT=/dev/null sh -c "$step") >/dev/null
  patch="$repo/_out/fragments/packages-apps-foo.patch"
  grep -qF 'sha256:changed' "$patch"
  grep -qF 'sha256:created' "$patch"
  if grep -qF 'stray' "$patch"; then
    echo "the fragment carries a file outside the package being built" >&2
    exit 1
  fi

  rm -rf "$dir"
}
