#!/bin/sh
# Close a pull request opened by hand against a release branch by someone
# outside the team. Fixes reach the release lines through the backport batch
# (docs/release/backports.md), so a hand-made backport is work nobody needs; a
# fix that exists only on a release line carries the `release-only` label.
#
# Usage: close-manual-backport.sh [--dry-run] [EVENT_JSON]
#        close-manual-backport.sh [--dry-run] --pr NUMBER
#
# EVENT_JSON defaults to $GITHUB_EVENT_PATH and is a pull_request or
# pull_request_target payload. --pr reads the same REST pull request object
# with `gh api`, because `gh pr view --json` has no author association. Only
# JSON is read, that object and the author's repository permission: nothing
# from the pull request is checked out or run.
#
# The pull request is closed only when it is open, targets release-*, its
# author's association is not OWNER, MEMBER or COLLABORATOR, its author is not
# a bot, it carries no `release-only` label, and GitHub answers that the
# author's permission and role on the repository are both read or none. A
# missing field, a failed call, or a value this script does not know leaves it
# open and exits 0. Exits 1 when the input cannot be read, 2 on a usage error,
# and with gh's status when the close fails.
set -eu

usage() {
  echo "usage: close-manual-backport.sh [--dry-run] [EVENT_JSON | --pr NUMBER]" >&2
  exit 2
}

leave() {
  echo "close-manual-backport: $*; leaving it open."
  exit 0
}

dry_run=false
pr_number=
event=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) dry_run=true ;;
    --pr)
      [ "$#" -ge 2 ] || usage
      pr_number=$2
      shift
      ;;
    -*) usage ;;
    *)
      [ -z "$event" ] || usage
      event=$1
      ;;
  esac
  shift
done

command -v jq >/dev/null 2>&1 || { echo "close-manual-backport: jq is required" >&2; exit 1; }

if [ -n "$pr_number" ]; then
  [ -z "$event" ] || usage
  case "$pr_number" in '' | *[!0-9]*) usage ;; esac
  if ! pr=$(gh api "repos/{owner}/{repo}/pulls/$pr_number"); then
    echo "close-manual-backport: cannot read PR #$pr_number with gh" >&2
    exit 1
  fi
else
  event=${event:-${GITHUB_EVENT_PATH:-}}
  [ -n "$event" ] || usage
  if ! pr=$(jq -c '.pull_request' "$event" 2>/dev/null); then
    echo "close-manual-backport: cannot read the event JSON at $event" >&2
    exit 1
  fi
fi

# A filter that fails on an unexpected shape prints nothing, which every check
# below reads as a missing field.
json_field() {
  printf '%s' "$1" | jq -r "$2" 2>/dev/null || true
}

field() {
  json_field "$pr" "$1"
}

[ "$(field 'type')" = object ] || leave "the input carries no pull request"
number=$(field '.number | select(type == "number" and . > 0 and . == floor) | tostring')
[ -n "$number" ] || leave "the pull request carries no number"

base=$(field '.base.ref | strings')
case "$base" in
  release-*) ;;
  '') leave "PR #$number carries no base ref" ;;
  *) leave "PR #$number targets $base, not a release branch" ;;
esac

state=$(field '.state | strings')
[ "$state" = open ] || leave "PR #$number is not open (state: ${state:-missing})"

assoc=$(field '.author_association | strings')
case "$assoc" in
  OWNER | MEMBER | COLLABORATOR) leave "PR #$number is from a repository $assoc" ;;
  CONTRIBUTOR | FIRST_TIME_CONTRIBUTOR | FIRST_TIMER | NONE) ;;
  '') leave "PR #$number carries no author association" ;;
  *) leave "PR #$number has an author association this script does not know ($assoc)" ;;
esac

user_type=$(field '.user.type | strings')
case "$user_type" in
  User) ;;
  Bot) leave "PR #$number is from a bot" ;;
  '') leave "PR #$number carries no author type" ;;
  *) leave "PR #$number has an author type this script does not know ($user_type)" ;;
esac

labels_ok=$(field 'select((.labels | type) == "array" and all(.labels[]; (.name? | type) == "string")) | "yes"')
[ "$labels_ok" = yes ] || leave "PR #$number carries no readable label list"
release_only=$(field 'any(.labels[]; .name == "release-only")')
[ "$release_only" = false ] || leave "PR #$number carries the release-only label"

repo=$(field '.base.repo.full_name | strings')
case "$repo" in
  */*/* | *[!A-Za-z0-9._/-]* | /* | */) leave "PR #$number names no usable base repository" ;;
  */*) ;;
  *) leave "PR #$number names no usable base repository" ;;
esac
login=$(field '.user.login | strings')
case "$login" in
  '' | *[!A-Za-z0-9-]*) leave "PR #$number carries no usable author login" ;;
esac

# author_association reports an organization member whose membership is
# private as a CONTRIBUTOR, so the author's own access to the repository is the
# check that decides. `permission` folds maintain into write and triage into
# read, which is why `role_name` has to agree.
if ! access=$(gh api "repos/$repo/collaborators/$login/permission"); then
  leave "PR #$number: cannot read $login's permission on $repo"
fi
permission=$(json_field "$access" '.permission | strings')
role=$(json_field "$access" '.role_name | strings')
case "$permission:$role" in
  read:read | read:none | none:read | none:none) ;;
  *) leave "PR #$number is from $login, whose permission on $repo is ${permission:-unknown} (role ${role:-unknown})" ;;
esac

policy="https://github.com/$repo/blob/main/docs/release/backports.md"
comment="Thank you for the contribution! Backports are automatic: fixes merged to \`main\` reach the supported release lines on their own, as the [backport policy]($policy) describes, so this pull request is closed. If the fix only makes sense on a release line and has no counterpart on \`main\`, please ask a maintainer to add the \`release-only\` label and reopen it."

if [ "$dry_run" = true ]; then
  printf 'close-manual-backport: would close PR #%s in %s with this comment:\n%s\n' "$number" "$repo" "$comment"
  exit 0
fi
gh pr close "$number" --repo "$repo" --comment "$comment"
echo "close-manual-backport: closed PR #$number in $repo."
