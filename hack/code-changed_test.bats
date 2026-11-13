#!/usr/bin/env bats
# Unit tests for hack/code-changed.sh, the PR docs-only gate, and for the
# paths-ignore lists that mirror it in the push-triggered workflows.
#
# Run from the repo root through `make bats-unit-tests`; the relative paths
# below resolve against that cwd. Negated assertions use `if cmd; then false`
# rather than `! cmd`; see hack/bats-no-negated-assert.bats.

load test_helper

classify() {
  list=$(mktemp)
  printf '%s\n' "$@" > "$list"
  hack/code-changed.sh "$list"
  rm -f "$list"
}

@test "docs tree, Markdown and repo metadata are not code" {
  [ "$(classify docs/release.md docs/img/arch.png)" = "false" ]
  [ "$(classify README.md ADOPTERS.md CLAUDE.md hack/README.md)" = "false" ]
  [ "$(classify packages/apps/mariadb/README.md packages/system/multus/README.md)" = "false" ]
  [ "$(classify examples/backups/etcd/README.md examples/backups/etcd/91-scenario-user-backup.md)" = "false" ]
  [ "$(classify img/logo.svg LICENSE security-insights.yml .coderabbit.yaml .gemini/styleguide.md)" = "false" ]
  [ "$(classify .github/CODEOWNERS .github/ISSUE_TEMPLATE/bug_report.md .github/ISSUE_TEMPLATE/config.yml .github/PULL_REQUEST_TEMPLATE.md .github/labels.yml .github/renovate.json)" = "false" ]
}

@test "any product path makes the list code" {
  [ "$(classify docs/release.md packages/apps/mariadb/values.yaml)" = "true" ]
  [ "$(classify README.md hack/code-changed.sh)" = "true" ]
  [ "$(classify .github/workflows/pull-requests.yaml)" = "true" ]
  [ "$(classify .github/workflows/README.md .github/actionlint.yaml)" = "true" ]
  [ "$(classify examples/backups/etcd/run-all.sh)" = "true" ]
  [ "$(classify packages/tests/fixture/values.yaml)" = "true" ]
  [ "$(classify .gitignore)" = "true" ]
  [ "$(classify .dockerignore)" = "true" ]
}

@test "a Markdown suffix on a directory does not hide the files under it" {
  [ "$(classify packages/apps/foo.md/templates/deploy.yaml)" = "true" ]
  [ "$(classify docs.md/x.go)" = "true" ]
}

@test "a rename listed as both paths reads as code when either side is code" {
  # git diff --no-renames and the fork lane both list old and new path.
  [ "$(classify packages/apps/foo/templates/deploy.yaml docs/deploy.yaml)" = "true" ]
  [ "$(classify docs/old.md docs/new.md)" = "false" ]
}

@test "an empty list is not code and a blank line fails closed" {
  list=$(mktemp)
  : > "$list"
  [ "$(hack/code-changed.sh "$list")" = "false" ]
  printf 'docs/a.md\n\n' > "$list"
  [ "$(hack/code-changed.sh "$list")" = "true" ]
  rm -f "$list"
}

@test "usage errors exit 2" {
  run hack/code-changed.sh
  [ "$status" -eq 2 ]
  run hack/code-changed.sh /nonexistent/changed.txt
  [ "$status" -eq 2 ]
}

# The push-triggered workflows cannot call the script, so they carry a
# paths-ignore glob list. Every glob must name something the script also calls
# non-code, or a push of real code would skip its build; and the three lists
# must be identical, so one copy cannot drift from the others.
paths_ignore() {
  yq "$1" "$2"
}

@test "paths-ignore lists mirror the classifier and each other" {
  main=$(paths_ignore '.on.push.paths-ignore[]' .github/workflows/build-main.yaml)
  [ -n "$main" ]
  [ "$(paths_ignore '.on.push.paths-ignore[]' .github/workflows/build-release.yaml)" = "$main" ]
  [ "$(paths_ignore '.on.push.paths-ignore[]' .github/workflows/codeql.yml)" = "$main" ]
  [ "$(paths_ignore '.on.pull_request.paths-ignore[]' .github/workflows/codeql.yml)" = "$main" ]
  # Turn each glob into a representative path: `**` spans directories, `*` a name.
  samples=$(printf '%s\n' "$main" | sed -e 's#/\*\*$#/sub/dir/file.txt#' -e 's#^\*\*\.md$#packages/apps/x/README.md#' -e 's#\*\*#sub/dir#g' -e 's#\*#name#g')
  echo "$samples"
  list=$(mktemp)
  printf '%s\n' "$samples" > "$list"
  [ "$(hack/code-changed.sh "$list")" = "false" ]
  rm -f "$list"
}
