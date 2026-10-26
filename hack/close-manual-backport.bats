#!/usr/bin/env bats
# Tests for hack/close-manual-backport.sh, which closes a pull request opened by
# hand against a release branch by someone outside the team.
#
# gh is stubbed: every call is recorded, and `gh api` answers with $PR_JSON. A
# case that expects the pull request to stay open asserts that gh recorded
# nothing at all, so a close that slips past a guard cannot go unseen.
#
# Run with: bats hack/close-manual-backport.bats

load test_helper

SCRIPT="${BATS_TEST_DIRNAME}/close-manual-backport.sh"
POLICY_URL="https://github.com/cozystack/cozystack/blob/main/docs/release/backports.md"

_stub_gh() {
  mkdir -p "$1"
  cat > "$1/gh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$GH_CALLS"
if [ "$1" = api ]; then
  cat "$PR_JSON"
fi
EOF
  chmod +x "$1/gh"
}

setup() {
  strict_setup
  _stub_gh "${BATS_TEST_TMPDIR}/bin"
  export PATH="${BATS_TEST_TMPDIR}/bin:$PATH"
  export GH_CALLS="${BATS_TEST_TMPDIR}/calls"
  export PR_JSON="${BATS_TEST_TMPDIR}/pr.json"
  EVENT="${BATS_TEST_TMPDIR}/event.json"
  # A run inside GitHub Actions has its own event here; never read it.
  export GITHUB_EVENT_PATH="$EVENT"
  : > "$GH_CALLS"
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

@test "closes a pull request to release-1.6 from outside the team, linking the policy" {
  for assoc in CONTRIBUTOR FIRST_TIME_CONTRIBUTOR FIRST_TIMER NONE; do
    : > "$GH_CALLS"
    make_event release-1.6 "$assoc" User '["kind/bug"]'
    run "$SCRIPT" "$EVENT"
    [ "$status" -eq 0 ] || { echo "FAIL: $assoc: exit $status: $output"; false; }
    [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 1 ] || { echo "FAIL: $assoc:"; cat "$GH_CALLS"; false; }
    grep -q '^pr close 42 --repo cozystack/cozystack --comment Thank you' "$GH_CALLS"
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
}

@test "--dry-run never calls a write verb" {
  make_event release-1.6 CONTRIBUTOR User '[]'

  run "$SCRIPT" --dry-run "$EVENT"
  [ "$status" -eq 0 ]
  if [ -s "$GH_CALLS" ]; then echo "FAIL: gh was called:"; cat "$GH_CALLS"; false; fi
  case "$output" in
    *"would close PR #42 in cozystack/cozystack"*"$POLICY_URL"*) ;;
    *) echo "FAIL: dry run did not report the close: $output"; false ;;
  esac

  # By number the one gh call allowed is the read.
  run "$SCRIPT" --dry-run --pr 42
  [ "$status" -eq 0 ]
  [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 1 ]
  grep -qx 'api repos/{owner}/{repo}/pulls/42' "$GH_CALLS"
  case "$output" in
    *"would close PR #42"*) ;;
    *) echo "FAIL: dry run did not report the close: $output"; false ;;
  esac
}

@test "--pr reads the pull request with gh and closes it" {
  make_event release-1.6 CONTRIBUTOR User '[]'
  run "$SCRIPT" --pr 42
  [ "$status" -eq 0 ]
  [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 2 ]
  [ "$(sed -n 1p "$GH_CALLS")" = 'api repos/{owner}/{repo}/pulls/42' ]
  sed -n 2p "$GH_CALLS" | grep -q '^pr close 42 --repo cozystack/cozystack --comment '
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
