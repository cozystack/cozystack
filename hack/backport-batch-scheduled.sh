#!/usr/bin/env bash
# The scheduled backport batch (docs/release/backports.md): for each line the
# batch runs for, newest first, continue the open batch pull request or open
# one, check the line, and report what waits on the batch. This is what
# .github/workflows/backport-batch.yaml runs; a maintainer can run it by hand
# from a clean worktree of the repository.
#
# Usage: backport-batch-scheduled.sh [--dry-run]
#
# --dry-run builds and checks each batch in this worktree and prints what it
# would report, but pushes nothing, opens or edits no pull request, writes no
# comment and starts no agent task. Its check sees the line without the batch
# it just built, so it can exit 1 where the real run's check passes. Without
# --dry-run the batch's token has to be in BACKPORT_BATCH_TOKEN, and it is the
# only token gh sees: the job's own GITHUB_TOKEN is never a fallback, because a
# push made with it starts no CI on the batch pull request.
#
# Environment:
#   BACKPORT_BATCH        the backport-batch binary (default: built from this tree)
#   BACKPORT_BATCH_TOKEN  the token the batch pushes, opens and comments with
#   BACKPORT_AGENT        ci or manual (default manual): who takes a deferred
#                         conflict, the CI agent or a maintainer
#   BACKPORT_AGENT_TOKEN  with ci, the token the CI agent's tasks start with
#   BACKPORT_ALERT_TEAM   the team the batch's comment mentions, org/team;
#                         nobody is mentioned when it is unset
#   GITHUB_STEP_SUMMARY   when set, each finding is appended to it as a line
#
# Exits 0 when every line ran and its check passed, 1 when a line's run,
# check or report failed, and 2 on a usage or setup error.
set -euo pipefail

die() {
  echo "backport-batch-scheduled: $*" >&2
  exit 2
}

usage="usage: backport-batch-scheduled.sh [--dry-run]"
dry_run=false
[ "$#" -le 1 ] || die "$usage"
case "${1:-}" in
  "") ;;
  --dry-run) dry_run=true ;;
  *) die "$usage" ;;
esac

agent=${BACKPORT_AGENT:-manual}
case "$agent" in
  ci | manual) ;;
  *) die "BACKPORT_AGENT is '$agent'; set the repository variable to ci or manual" ;;
esac

push=()
if ! $dry_run; then
  [ -n "${BACKPORT_BATCH_TOKEN:-}" ] ||
    die "BACKPORT_BATCH_TOKEN is empty; set the repository secret to the batch's token (docs/release/backports.md#the-scheduled-run). The job's GITHUB_TOKEN is not used: its pushes start no CI."
  if [ "$agent" = ci ] && [ -z "${BACKPORT_AGENT_TOKEN:-}" ]; then
    die "BACKPORT_AGENT is ci, but BACKPORT_AGENT_TOKEN is empty; set the secret, or set BACKPORT_AGENT to manual"
  fi
  export GH_TOKEN="$BACKPORT_BATCH_TOKEN"
  unset GITHUB_TOKEN
  push=(--push)
fi

mention=()
if [ -n "${BACKPORT_ALERT_TEAM:-}" ]; then
  mention=(--mention "$BACKPORT_ALERT_TEAM")
fi

cd "$(git rev-parse --show-toplevel)"
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
  "$bin" run --line "$line" --continue "${push[@]}" > "$out/$line.md" || rc=$?
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
  notes=$("$bin" notify --line "$line" --agent "$agent" "${mention[@]}" "${push[@]}") || rc=$?
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
