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
  [ "$(classify examples/backups/etcd/README.md examples/backups/etcd/91-scenario-user-backup.md)" = "false" ]
  [ "$(classify img/logo.svg LICENSE security-insights.yml .coderabbit.yaml .gemini/styleguide.md)" = "false" ]
  [ "$(classify .github/CODEOWNERS .github/ISSUE_TEMPLATE/bug_report.md .github/ISSUE_TEMPLATE/config.yml .github/PULL_REQUEST_TEMPLATE.md .github/labels.yml .github/renovate.json)" = "false" ]
}

@test "Markdown under packages/ is code: Go tests read chart READMEs" {
  # internal/controller/fluxplunger/hook_contract_test.go reads this README.
  [ "$(classify packages/system/flux-plunger/README.md)" = "true" ]
  [ "$(classify packages/apps/mariadb/README.md docs/release.md)" = "true" ]
  [ "$(classify hack/README.md examples/backups/etcd/README.md)" = "false" ]
}

@test "CI-only Go tools are not code, the rest of cmd/ is" {
  [ "$(classify cmd/backport-batch/main.go cmd/api-gate/main.go)" = "false" ]
  [ "$(classify cmd/backport-batch/main.go cmd/cozystack-api/main.go)" = "true" ]
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

# The push-triggered workflows cannot call the script, so they carry a `paths`
# filter: `**`, then one `!` glob per non-code path, then re-includes. Every `!`
# glob must name something the script calls non-code, or a push of real code
# would skip its build; every re-include must name code; and the three filters
# must be identical, so one copy cannot drift from the others.
push_paths() {
  yq '.on.push.paths[]' "$1"
}

# A representative path for a glob: `**` spans directories, `*` a name.
samples() {
  sed -e 's#/\*\*$#/sub/dir/file.txt#' -e 's#^\*\*\.md$#examples/x/README.md#' -e 's#\*\*#sub/dir#g' -e 's#\*#name#g'
}

@test "push path filters mirror the classifier and each other" {
  main=$(push_paths .github/workflows/build-main.yaml)
  [ "$(printf '%s\n' "$main" | head -n 1)" = "**" ]
  [ "$(push_paths .github/workflows/build-release.yaml)" = "$main" ]
  [ "$(push_paths .github/workflows/codeql.yml)" = "$main" ]
  # Chart READMEs are code (see code-changed.sh), so `!**.md` needs this back.
  printf '%s\n' "$main" | command grep -qxF 'packages/**.md'
  list=$(mktemp)
  printf '%s\n' "$main" | sed -n 's/^!//p' | samples > "$list"
  cat "$list"
  [ -s "$list" ]
  [ "$(hack/code-changed.sh "$list")" = "false" ]
  for glob in $(printf '%s\n' "$main" | sed -e '1d' -e '/^!/d'); do
    printf '%s\n' "$glob" | samples > "$list"
    cat "$list"
    [ "$(hack/code-changed.sh "$list")" = "true" ]
  done
  rm -f "$list"
}

@test "CodeQL's pull_request trigger has no path filter: it is a required check" {
  # Only `branches`: any paths or paths-ignore key would leave PRs on "Expected".
  [ "$(yq '.on.pull_request | keys | join(",")' .github/workflows/codeql.yml)" = "branches" ]
}
