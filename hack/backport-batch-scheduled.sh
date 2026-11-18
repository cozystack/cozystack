#!/usr/bin/env bash
# The scheduled backport batch (docs/release/backports.md): for each line the
# batch runs for, newest first, continue the open batch pull request or open
# one, check the line, and report what waits on the batch. This is what
# .github/workflows/backport-batch.yaml runs; a maintainer can run it by hand
# from a clean worktree of the repository.
#
# Usage: backport-batch-scheduled.sh [--dry-run] [--no-workflow-files]
#
# --dry-run builds and checks each batch in this worktree and prints what it
# would report, but pushes nothing, opens or edits no pull request and writes
# no comment. Its check sees the line without the batch it just built, so it
# can exit 1 where the real run's check passes. Without --dry-run the batch's
# token has to be in BACKPORT_BATCH_TOKEN, and it is the only token gh sees:
# the job's own GITHUB_TOKEN is never a fallback, because a push made with it
# starts no CI on the batch pull request. The workflow passes the cozystack-ci
# App's token; a maintainer running this by hand passes their own.
#
# --no-workflow-files goes to run: an original that changes .github/workflows/
# is left on the batch pull request for a maintainer instead of picked, for a
# token that may not push such a commit, as the workflow's is.
#
# Environment:
#   BACKPORT_BATCH        the backport-batch binary (default: built from this tree)
#   BACKPORT_BATCH_TOKEN  the token the batch pushes, opens and comments with
#   BACKPORT_BATCH_BOT    the bot the scheduled batch writes as, <app>[bot],
#                         whose batch pull request and comment are trusted;
#                         cozystack-ci[bot] when unset
#   BACKPORT_ALERT_TEAM   the team or person the batch's comment mentions,
#                         org/team or a login; nobody is mentioned when it is
#                         unset
#   GITHUB_STEP_SUMMARY   when set, each finding is appended to it as a line
#
# Exits 0 when every line ran and its check passed, 1 when a line's run,
# check or report failed, and 2 on a usage or setup error.
set -euo pipefail

die() {
  echo "backport-batch-scheduled: $*" >&2
  exit 2
}

usage="usage: backport-batch-scheduled.sh [--dry-run] [--no-workflow-files]"
dry_run=false
# An empty array expands under set -u only as ${a[@]+"${a[@]}"} on bash before
# 4.4, which is what macOS ships.
workflows=()
for arg in "$@"; do
  case "$arg" in
    --dry-run) dry_run=true ;;
    --no-workflow-files) workflows=(--no-workflow-files) ;;
    *) die "$usage" ;;
  esac
done

push=()
if ! $dry_run; then
  [ -n "${BACKPORT_BATCH_TOKEN:-}" ] ||
    die "BACKPORT_BATCH_TOKEN is empty; the workflow sets it to the cozystack-ci App's token, a run by hand to your own (docs/release/backports.md#the-scheduled-run). The job's GITHUB_TOKEN is not used: its pushes start no CI."
  export GH_TOKEN="$BACKPORT_BATCH_TOKEN"
  unset GITHUB_TOKEN
  push=(--push)
fi

mention=()
if [ -n "${BACKPORT_ALERT_TEAM:-}" ]; then
  mention=(--mention "$BACKPORT_ALERT_TEAM")
fi

top=$(git rev-parse --show-toplevel) || die "not in a worktree of the repository"
cd "$top"
out=$(mktemp -d)
bin=${BACKPORT_BATCH:-}
if [ -z "$bin" ]; then
  # Built before the first run switches this tree to a batch branch, whose
  # line may not have the tool at all.
  bin="$out/backport-batch"
  go build -o "$bin" ./cmd/backport-batch
fi

report() {
  echo "$1"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    printf -- '- %s\n' "$1" >> "$GITHUB_STEP_SUMMARY"
  fi
}

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  printf '## Backport batch\n\n' >> "$GITHUB_STEP_SUMMARY"
fi
lines=$("$bin" lines) || die "cannot list the release lines"
[ -n "$lines" ] || die "no release line to run the batch for"

status=0
for line in $lines; do
  echo "::group::$line"
  rc=0
  "$bin" run --line "$line" --continue ${workflows[@]+"${workflows[@]}"} ${push[@]+"${push[@]}"} > "$out/$line.md" || rc=$?
  if [ "$rc" -ne 0 ]; then
    report "$line: the run failed with exit $rc"
    status=1
  elif $dry_run; then
    report "$line: the batch is built in this tree, its body in $out/$line.md"
  fi
  rc=0
  "$bin" check --line "$line" || rc=$?
  if [ "$rc" -ne 0 ]; then
    report "$line: the check exited $rc; see its output above"
    status=1
  fi
  rc=0
  notes=$("$bin" notify --line "$line" ${mention[@]+"${mention[@]}"} ${push[@]+"${push[@]}"}) || rc=$?
  while IFS= read -r note; do
    [ -z "$note" ] || report "$note"
  done <<< "$notes"
  if [ "$rc" -ne 0 ]; then
    report "$line: reporting what waits on the batch failed with exit $rc"
    status=1
  fi
  echo "::endgroup::"
done
exit "$status"
