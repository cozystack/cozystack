#!/usr/bin/env bats
# Tests for hack/close-manual-backport.sh, which closes a pull request opened by
# hand against a release branch by someone outside the team.
#
# gh is stubbed: every call is recorded, the permission endpoint answers with
# $PERM_JSON (or fails under PERM_FAIL), and any other `gh api` with $PR_JSON.
# A case that expects the pull request to stay open asserts exactly which reads
# gh recorded, so a close that slips past a guard cannot go unseen.
#
# Run with: bats hack/close-manual-backport.bats

load test_helper

SCRIPT="${BATS_TEST_DIRNAME}/close-manual-backport.sh"
WF="${BATS_TEST_DIRNAME}/../.github/workflows/pr-labeler.yaml"
LABELS="${BATS_TEST_DIRNAME}/../.github/labels.yml"
POLICY_URL="https://github.com/cozystack/cozystack/blob/main/docs/release/backports.md"
PERM_CALL="api repos/cozystack/cozystack/collaborators/someone/permission"

# The close-manual-backport job of pr-labeler.yaml, comment lines dropped so a
# setting demoted to a comment cannot satisfy an assertion.
guard_job() {
  awk '/^  close-manual-backport:$/ { f = 1; next } f && /^  [^ ]/ { exit } f' "$WF" \
    | grep -v '^[[:space:]]*#'
}

_stub_gh() {
  mkdir -p "$1"
  cat > "$1/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_CALLS"
case "$1 $2" in
  "api repos/"*/collaborators/*/permission)
    if [ -n "${PERM_FAIL:-}" ]; then
      echo 'gh: Not Found (HTTP 404)' >&2
      exit 1
    fi
    cat "$PERM_JSON"
    ;;
  "api "*) cat "$PR_JSON" ;;
esac
EOF
  chmod +x "$1/gh"
}

setup() {
  strict_setup
  _stub_gh "${BATS_TEST_TMPDIR}/bin"
  export PATH="${BATS_TEST_TMPDIR}/bin:$PATH"
  export GH_CALLS="${BATS_TEST_TMPDIR}/calls"
  export PR_JSON="${BATS_TEST_TMPDIR}/pr.json"
  export PERM_JSON="${BATS_TEST_TMPDIR}/perm.json"
  unset PERM_FAIL
  set_permission read read
  EVENT="${BATS_TEST_TMPDIR}/event.json"
  # A run inside GitHub Actions has its own event here; never read it.
  export GITHUB_EVENT_PATH="$EVENT"
  : > "$GH_CALLS"
}

# The permission endpoint's answer: legacy permission, then role_name.
set_permission() {
  printf '{"permission":"%s","role_name":"%s","user":{"login":"someone"}}\n' "$1" "$2" > "$PERM_JSON"
}

# Writes a pull_request_target event to $EVENT and its pull request object to
# $PR_JSON. Arguments: base ref, author association, author type, labels as a
# JSON array of names. "-" leaves the association or the type out.
make_event() {
  jq -n --arg base "$1" --arg assoc "$2" --arg type "$3" --argjson labels "$4" '
    ({number: 42, state: "open",
      base: {ref: $base, repo: {full_name: "cozystack/cozystack"}},
      user: {login: "someone", type: $type},
      author_association: $assoc,
      labels: ($labels | map({name: .}))}
     | if $assoc == "-" then del(.author_association) else . end
     | if $type == "-" then del(.user.type) else . end) as $pr
    | {action: "opened", number: 42, pull_request: $pr,
       repository: {full_name: "cozystack/cozystack"}}' > "$EVENT"
  jq '.pull_request' "$EVENT" > "$PR_JSON"
}

assert_left_open() {
  [ "$status" -eq 0 ] || { echo "FAIL: exit $status: $output"; false; }
  if [ -s "$GH_CALLS" ]; then
    echo "FAIL: gh was called:"
    cat "$GH_CALLS"
    false
  fi
  case "$output" in
    *"leaving it open"*) ;;
    *) echo "FAIL: no reason given: $output"; false ;;
  esac
}

# Left open after the permission read, which is then the only gh call.
assert_left_open_after_permission() {
  [ "$status" -eq 0 ] || { echo "FAIL: exit $status: $output"; false; }
  [ "$(cat "$GH_CALLS")" = "$PERM_CALL" ] || { echo "FAIL: gh calls:"; cat "$GH_CALLS"; false; }
  case "$output" in
    *"leaving it open"*) ;;
    *) echo "FAIL: no reason given: $output"; false ;;
  esac
}

@test "closes a pull request to release-1.6 from outside the team, linking the policy" {
  for assoc in CONTRIBUTOR FIRST_TIME_CONTRIBUTOR FIRST_TIMER NONE; do
    : > "$GH_CALLS"
    make_event release-1.6 "$assoc" User '["kind/bug"]'
    run "$SCRIPT" "$EVENT"
    [ "$status" -eq 0 ] || { echo "FAIL: $assoc: exit $status: $output"; false; }
    [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 2 ] || { echo "FAIL: $assoc:"; cat "$GH_CALLS"; false; }
    [ "$(sed -n 1p "$GH_CALLS")" = "$PERM_CALL" ]
    sed -n 2p "$GH_CALLS" | grep -q '^pr close 42 --repo cozystack/cozystack --comment Thank you'
    grep -qF "$POLICY_URL" "$GH_CALLS"
    grep -qF '`release-only` label' "$GH_CALLS"
    if grep -qF '—' "$GH_CALLS"; then echo "FAIL: the comment carries an em dash"; false; fi
  done
}

@test "leaves OWNER, MEMBER and COLLABORATOR pull requests open" {
  for assoc in OWNER MEMBER COLLABORATOR; do
    : > "$GH_CALLS"
    make_event release-1.6 "$assoc" User '[]'
    run "$SCRIPT" "$EVENT"
    assert_left_open
  done
}

@test "leaves a bot's pull request open" {
  make_event release-1.6 NONE Bot '[]'
  run "$SCRIPT" "$EVENT"
  assert_left_open
}

@test "leaves a pull request labeled release-only open" {
  make_event release-1.6 CONTRIBUTOR User '["kind/bug","release-only"]'
  run "$SCRIPT" "$EVENT"
  assert_left_open
}

@test "leaves pull requests to main and to other branches open" {
  for base in main feature-release-1.6 releases; do
    : > "$GH_CALLS"
    make_event "$base" CONTRIBUTOR User '[]'
    run "$SCRIPT" "$EVENT"
    assert_left_open
  done
}

@test "does nothing when the author association is missing" {
  make_event release-1.6 - User '[]'
  run "$SCRIPT" "$EVENT"
  assert_left_open
  case "$output" in
    *"no author association"*) ;;
    *) echo "FAIL: reason does not name the association: $output"; false ;;
  esac
}

@test "does nothing on an unknown association, a missing author type or no label list" {
  make_event release-1.6 SOMETHING_NEW User '[]'
  run "$SCRIPT" "$EVENT"
  assert_left_open

  : > "$GH_CALLS"
  make_event release-1.6 CONTRIBUTOR - '[]'
  run "$SCRIPT" "$EVENT"
  assert_left_open

  : > "$GH_CALLS"
  make_event release-1.6 CONTRIBUTOR User '[]'
  jq '.pull_request |= del(.labels)' "$EVENT" > "$EVENT.tmp"
  mv "$EVENT.tmp" "$EVENT"
  run "$SCRIPT" "$EVENT"
  assert_left_open

  # A label whose name cannot be read could be release-only.
  : > "$GH_CALLS"
  make_event release-1.6 CONTRIBUTOR User '[]'
  jq '.pull_request.labels = [{"id": 1}]' "$EVENT" > "$EVENT.tmp"
  mv "$EVENT.tmp" "$EVENT"
  run "$SCRIPT" "$EVENT"
  assert_left_open

  # The login goes into the API path, so only a GitHub login shape is asked about.
  : > "$GH_CALLS"
  make_event release-1.6 CONTRIBUTOR User '[]'
  jq '.pull_request.user.login = "someone/../../orgs/x"' "$EVENT" > "$EVENT.tmp"
  mv "$EVENT.tmp" "$EVENT"
  run "$SCRIPT" "$EVENT"
  assert_left_open
}

# author_association shows an organization member whose membership is private
# as a CONTRIBUTOR. `permission` alone would also pass a triage member, which
# GitHub folds into read.
@test "a private member reported as CONTRIBUTOR stays open when their access is above read" {
  for answer in "write write" "admin admin" "write maintain" "read triage" "read some-custom-role"; do
    : > "$GH_CALLS"
    set -- $answer
    set_permission "$1" "$2"
    make_event release-1.6 CONTRIBUTOR User '[]'
    run "$SCRIPT" "$EVENT"
    assert_left_open_after_permission
    case "$output" in
      *"permission on cozystack/cozystack is $1 (role $2)"*) ;;
      *) echo "FAIL: reason does not name the access ($answer): $output"; false ;;
    esac
  done
}

@test "a failed or unexpected permission answer leaves it open" {
  make_event release-1.6 CONTRIBUTOR User '[]'

  export PERM_FAIL=1
  run "$SCRIPT" "$EVENT"
  assert_left_open_after_permission
  case "$output" in
    *"cannot read someone's permission"*) ;;
    *) echo "FAIL: reason does not name the failed read: $output"; false ;;
  esac
  unset PERM_FAIL

  for answer in 'not json' '{}' '{"permission":"read"}' '{"role_name":"read"}' '{"permission":"read","role_name":null}' '[]'; do
    : > "$GH_CALLS"
    printf '%s\n' "$answer" > "$PERM_JSON"
    run "$SCRIPT" "$EVENT"
    assert_left_open_after_permission
  done
}

@test "an author with read or no access is closed" {
  for answer in "read read" "none none" "read none" "none read"; do
    : > "$GH_CALLS"
    set -- $answer
    set_permission "$1" "$2"
    make_event release-1.6 CONTRIBUTOR User '[]'
    run "$SCRIPT" "$EVENT"
    [ "$status" -eq 0 ] || { echo "FAIL: $answer: exit $status: $output"; false; }
    [ "$(sed -n 1p "$GH_CALLS")" = "$PERM_CALL" ] || { echo "FAIL: $answer:"; cat "$GH_CALLS"; false; }
    sed -n 2p "$GH_CALLS" | grep -q '^pr close 42 --repo cozystack/cozystack --comment ' \
      || { echo "FAIL: $answer: not closed:"; cat "$GH_CALLS"; false; }
  done
}

@test "--dry-run never calls a write verb" {
  make_event release-1.6 CONTRIBUTOR User '[]'

  # From the event the one gh call allowed is the permission read.
  run "$SCRIPT" --dry-run "$EVENT"
  [ "$status" -eq 0 ]
  [ "$(cat "$GH_CALLS")" = "$PERM_CALL" ] || { echo "FAIL: gh calls:"; cat "$GH_CALLS"; false; }
  case "$output" in
    *"would close PR #42 in cozystack/cozystack"*"$POLICY_URL"*) ;;
    *) echo "FAIL: dry run did not report the close: $output"; false ;;
  esac

  # By number, the pull request read and the permission read.
  : > "$GH_CALLS"
  run "$SCRIPT" --dry-run --pr 42
  [ "$status" -eq 0 ]
  [ "$(cat "$GH_CALLS")" = "$(printf 'api repos/{owner}/{repo}/pulls/42\n%s' "$PERM_CALL")" ] \
    || { echo "FAIL: gh calls:"; cat "$GH_CALLS"; false; }
  case "$output" in
    *"would close PR #42"*) ;;
    *) echo "FAIL: dry run did not report the close: $output"; false ;;
  esac
}

@test "--pr reads the pull request with gh and closes it" {
  make_event release-1.6 CONTRIBUTOR User '[]'
  run "$SCRIPT" --pr 42
  [ "$status" -eq 0 ]
  [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 3 ]
  [ "$(sed -n 1p "$GH_CALLS")" = 'api repos/{owner}/{repo}/pulls/42' ]
  [ "$(sed -n 2p "$GH_CALLS")" = "$PERM_CALL" ]
  sed -n 3p "$GH_CALLS" | grep -q '^pr close 42 --repo cozystack/cozystack --comment '
}

@test "leaves a pull request that is already closed alone" {
  make_event release-1.6 CONTRIBUTOR User '[]'
  jq '.state = "closed"' "$PR_JSON" > "$PR_JSON.tmp"
  mv "$PR_JSON.tmp" "$PR_JSON"
  run "$SCRIPT" --pr 42
  [ "$status" -eq 0 ]
  [ "$(cat "$GH_CALLS")" = 'api repos/{owner}/{repo}/pulls/42' ]
  case "$output" in
    *"is not open"*) ;;
    *) echo "FAIL: $output"; false ;;
  esac
}

@test "an unreadable event is an error and closes nothing" {
  printf 'not json' > "$EVENT"
  run "$SCRIPT" "$EVENT"
  [ "$status" -eq 1 ]
  if [ -s "$GH_CALLS" ]; then echo "FAIL: gh was called:"; cat "$GH_CALLS"; false; fi
}

@test "a non-numeric --pr is a usage error" {
  run "$SCRIPT" --pr '42; gh pr close 1'
  [ "$status" -eq 2 ]
  if [ -s "$GH_CALLS" ]; then echo "FAIL: gh was called:"; cat "$GH_CALLS"; false; fi
}

# A renamed or deleted label is not an error anywhere: the guard would close
# every release-only pull request without a word.
@test "the release-only label the guard honours is defined in labels.yml" {
  grep -qF '.name == "release-only"' "$SCRIPT"
  grep -qx -- '- name: release-only' "$LABELS"
}

@test "pr-labeler runs the guard on opened and reopened pull requests to release-*" {
  job="$(guard_job)"
  [ -n "$job" ]
  printf '%s\n' "$job" | grep -qF "(github.event.action == 'opened' || github.event.action == 'reopened') &&"
  printf '%s\n' "$job" | grep -qF "startsWith(github.event.pull_request.base.ref, 'release-')"
  printf '%s\n' "$job" | grep -qxF '        run: hack/close-manual-backport.sh "$GITHUB_EVENT_PATH"'
}

# pull_request_target hands a write token to a fork's pull request. The job is
# safe only while it holds no more than it needs and never reaches the head.
@test "the guard job holds the least token and never checks out the pull request" {
  job="$(guard_job)"
  [ -n "$job" ]

  perms="$(printf '%s\n' "$job" | awk '/^    permissions:$/ { f = 1; next } f && /^    [^ ]/ { exit } f' \
    | sed 's/[[:space:]]*#.*$//' | sort)"
  [ "$perms" = "$(printf '      contents: read\n      pull-requests: write')" ] || {
    echo "FAIL: permissions are:"; printf '%s\n' "$perms"; false
  }

  # actions/checkout without a ref takes the event's own, which on
  # pull_request_target is the base repository; a ref is the way to the head.
  if printf '%s\n' "$job" | grep -qE '^[[:space:]]+ref:'; then echo "FAIL: the checkout names a ref"; false; fi
  printf '%s\n' "$job" | grep -qxF '          persist-credentials: false'

  # The token is the only expression: event fields reach the script through
  # the JSON file, never through the shell.
  [ "$(printf '%s\n' "$job" | grep -oF '${{' | wc -l | tr -d ' ')" -eq 1 ]
  printf '%s\n' "$job" | grep -qxF '          GH_TOKEN: ${{ github.token }}'
}
