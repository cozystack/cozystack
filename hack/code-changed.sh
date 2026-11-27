#!/bin/sh
# Classify a changed-path list for the PR pipeline: prints `true` when at least
# one path can reach the product (Go, charts, images, build inputs, workflows)
# and `false` when every path is documentation or repository metadata.
#
# This is the single source of truth for the docs-only gate: pull-requests.yaml
# `plan` feeds it the merge diff and e2e-fork.yaml `resolve` feeds it the
# base-repo PR file list. A rename must appear as BOTH paths (git diff
# --no-renames, or filename + previous_filename), so a packages/... -> docs/...
# move still reads as code. A blank line reads as code: fail closed.
#
# Non-code: the docs/ tree, Markdown outside packages/, images, LICENSE, GitHub
# and review-bot metadata. Markdown under packages/ stays code: Go controller
# tests read chart READMEs (internal/controller/fluxplunger/hook_contract_test.go
# checks packages/system/flux-plunger/README.md), and those run only when this
# prints `true`. The Bats lane of the `checks` job runs for every path except
# the CI-only tools in this list (cmd/backport-batch, cmd/api-gate), so non-code
# files still get their contract tests; they only skip build and e2e.
# The push `paths` filters in build-main.yaml, build-release.yaml and codeql.yml
# mirror this classifier (CodeQL's pull_request trigger has none: it is
# a required check); hack/code-changed_test.bats keeps them in step.
set -eu

if [ "$#" -ne 1 ] || [ ! -f "$1" ] || [ ! -r "$1" ]; then
  echo "usage: code-changed.sh <readable-changed-files>" >&2
  exit 2
fi

non_code_pattern='^(docs/|cmd/backport-batch/|cmd/api-gate/|img/|\.gemini/|\.github/ISSUE_TEMPLATE/|\.github/(CODEOWNERS|PULL_REQUEST_TEMPLATE\.md|labels\.yml|renovate\.json)$|\.coderabbit\.yaml$|security-insights\.yml$|LICENSE$)'

# ENVIRON, not -v: -v would expand the backslash escapes in the pattern.
pattern="$non_code_pattern" awk '
  !($0 ~ ENVIRON["pattern"] || ($0 ~ /\.md$/ && $0 !~ /^packages\//)) { code = 1 }
  END { print (code ? "true" : "false") }
' "$1"
