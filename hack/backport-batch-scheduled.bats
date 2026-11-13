#!/usr/bin/env bats
# Tests for hack/backport-batch-scheduled.sh, the scheduled backport batch, and
# for the workflow that runs it, .github/workflows/backport-batch.yaml.
#
# The backport-batch binary is stubbed through BACKPORT_BATCH: every call is
# recorded with the tokens gh would see, `lines` answers with $STUB_LINES, and
# a subcommand fails for the line named in $STUB_FAIL_<subcommand>.
#
# Run with: bats hack/backport-batch-scheduled.bats

load test_helper

SCRIPT="${BATS_TEST_DIRNAME}/backport-batch-scheduled.sh"
WF="${BATS_TEST_DIRNAME}/../.github/workflows/backport-batch.yaml"

setup() {
  strict_setup
  export TMPDIR="$BATS_TEST_TMPDIR"
  export CALLS="${BATS_TEST_TMPDIR}/calls"
  export BACKPORT_BATCH="${BATS_TEST_TMPDIR}/backport-batch"
  cat > "$BACKPORT_BATCH" <<'EOF'
#!/usr/bin/env bash
printf 'GH_TOKEN=%s GITHUB_TOKEN=%s %s\n' "${GH_TOKEN-unset}" "${GITHUB_TOKEN-unset}" "$*" >> "$CALLS"
line=${3:-}
case "$1" in
  lines) printf '%s\n' $STUB_LINES ;;
  run) [ "${STUB_FAIL_run:-}" != "$line" ] || exit 2 ;;
  check) [ "${STUB_FAIL_check:-}" != "$line" ] || exit 1 ;;
  notify)
    [ "${STUB_FAIL_notify:-}" != "$line" ] || exit 2
    echo "$line: something waits"
    ;;
esac
EOF
  chmod +x "$BACKPORT_BATCH"
  export STUB_LINES="release-1.7 release-1.6"
  unset STUB_FAIL_run STUB_FAIL_check STUB_FAIL_notify
  unset BACKPORT_ALERT_TEAM GITHUB_STEP_SUMMARY GH_TOKEN
  export GITHUB_TOKEN=job-token
  export BACKPORT_BATCH_TOKEN=batch-token
  : > "$CALLS"
}

# The calls recorded, without the token fields.
calls() {
  sed 's/^GH_TOKEN=[^ ]* GITHUB_TOKEN=[^ ]* //' "$CALLS"
}

@test "runs, checks and reports each line, newest first, pushing with the batch's token alone" {
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(calls)" = "lines
run --line release-1.7 --continue --push
check --line release-1.7
notify --line release-1.7 --push
run --line release-1.6 --continue --push
check --line release-1.6
notify --line release-1.6 --push" ]
  # Every call sees the batch's token, and the job's token is gone.
  [ "$(grep -c '^GH_TOKEN=batch-token GITHUB_TOKEN=unset ' "$CALLS")" -eq 7 ]
}

@test "a dry run pushes, opens and comments nothing, and needs no token" {
  unset BACKPORT_BATCH_TOKEN
  run "$SCRIPT" --dry-run
  [ "$status" -eq 0 ]
  if grep -q -- '--push' "$CALLS"; then
    echo "a dry run passed --push:"; cat "$CALLS"
    false
  fi
  [ "$(calls | grep -c '^run --line release-.* --continue$')" -eq 2 ]
  [ "$(calls | grep -c '^notify --line release-.*[0-9]$')" -eq 2 ]
}

@test "--no-workflow-files reaches run alone, and an unknown argument stops the script" {
  run "$SCRIPT" --no-workflow-files
  [ "$status" -eq 0 ]
  [ "$(calls | grep -c '^run --line release-.* --continue --no-workflow-files --push$')" -eq 2 ]
  if calls | grep -v '^run ' | grep -q -- '--no-workflow-files'; then
    echo "the flag reached another command:"; calls
    false
  fi

  : > "$CALLS"
  run "$SCRIPT" --dry-run --no-workflow-files
  [ "$status" -eq 0 ]
  [ "$(calls | grep -c '^run --line release-.* --continue --no-workflow-files$')" -eq 2 ]

  : > "$CALLS"
  run "$SCRIPT" --no-workflows
  [ "$status" -eq 2 ]
  [ ! -s "$CALLS" ]
}

@test "without BACKPORT_BATCH_TOKEN it stops before anything, even with the job's token" {
  unset BACKPORT_BATCH_TOKEN
  run "$SCRIPT"
  [ "$status" -eq 2 ]
  [[ "$output" == *"BACKPORT_BATCH_TOKEN is empty"* ]]
  [ ! -s "$CALLS" ]
}

@test "outside a worktree of the repository it stops before anything" {
  export GIT_CEILING_DIRECTORIES="$BATS_TEST_TMPDIR"
  mkdir "$BATS_TEST_TMPDIR/elsewhere"
  cd "$BATS_TEST_TMPDIR/elsewhere"
  run "$SCRIPT"
  [ "$status" -eq 2 ]
  [[ "$output" == *"not in a worktree of the repository"* ]]
  [ ! -s "$CALLS" ]
}

@test "the team is mentioned only when BACKPORT_ALERT_TEAM is set" {
  export BACKPORT_ALERT_TEAM=cozystack/maintainers
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  [ "$(calls | grep -c '^notify --line release-.* --mention cozystack/maintainers --push$')" -eq 2 ]

  export BACKPORT_ALERT_TEAM=
  : > "$CALLS"
  run "$SCRIPT"
  [ "$status" -eq 0 ]
  if grep -q -- '--mention' "$CALLS"; then
    echo "an empty team was mentioned:"; cat "$CALLS"
    false
  fi
}

@test "a line that fails goes into the summary, the other line still runs, and the job fails" {
  export GITHUB_STEP_SUMMARY="${BATS_TEST_TMPDIR}/summary"
  export STUB_FAIL_run=release-1.7 STUB_FAIL_check=release-1.6
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  [ "$(calls | grep -c '^run --line release-1.6 ')" -eq 1 ]
  grep -qx -- '- release-1.7: the run failed with exit 2' "$GITHUB_STEP_SUMMARY"
  grep -qx -- '- release-1.6: the check exited 1; see its output above' "$GITHUB_STEP_SUMMARY"
  grep -qx -- '- release-1.7: something waits' "$GITHUB_STEP_SUMMARY"

  # Each failure fails the job on its own.
  export STUB_FAIL_run=release-1.7 STUB_FAIL_check=
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  export STUB_FAIL_run= STUB_FAIL_check=release-1.6
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  export STUB_FAIL_check= STUB_FAIL_notify=release-1.6
  run "$SCRIPT"
  [ "$status" -eq 1 ]
  export STUB_FAIL_notify=
  run "$SCRIPT"
  [ "$status" -eq 0 ]
}

@test "the workflow runs Monday and Thursday as the cozystack-ci App and never with the job's token" {
  [ "$(yq -r '.on.schedule[0].cron' "$WF")" = "23 6 * * 1,4" ]
  [ "$(yq -r '.on.schedule | length' "$WF")" -eq 1 ]
  [ "$(yq -o=json -I=0 '.permissions' "$WF")" = "{}" ]
  [ "$(yq -o=json -I=0 '.jobs.batch.permissions' "$WF")" = '{"contents":"read"}' ]
  [ "$(yq -r '.jobs.batch.steps[] | select(.uses == "actions/checkout*") | .with."persist-credentials"' "$WF")" = "false" ]
  run_step='.jobs.batch.steps[] | select(.name == "Run the batch")'
  [ "$(yq -r "$run_step | .run" "$WF")" = "hack/backport-batch-scheduled.sh --no-workflow-files" ]
  [ "$(yq -r "$run_step | .env.BACKPORT_BATCH_TOKEN" "$WF")" = '${{ steps.app-token.outputs.token }}' ]
  [ "$(yq -r "$run_step | .env.BACKPORT_BATCH_BOT" "$WF")" = '${{ steps.app-token.outputs.app-slug }}[bot]' ]

  # The App token covers this repository alone, with Contents and Pull
  # requests and nothing else: no workflows, no packages.
  mint='.jobs.batch.steps[] | select(.id == "app-token")'
  [ "$(yq -r "$mint | .uses" "$WF")" = "actions/create-github-app-token@d72941d797fd3113feb6b93fd0dec494b13a2547" ]
  [ "$(yq -r "$mint | .with.owner" "$WF")" = "cozystack" ]
  [ "$(yq -r "$mint | .with.repositories" "$WF")" = "cozystack" ]
  [ "$(yq -r "$mint | .with | to_entries | map(select(.key | test(\"^permission-\"))) | map(.key + \"=\" + .value) | join(\",\")" "$WF")" = "permission-contents=write,permission-pull-requests=write" ]
  # A token minted after the checkout lives an hour; the job ends before.
  [ "$(yq -r '.jobs.batch."timeout-minutes"' "$WF")" -le 45 ]

  # The commits carry the App's bot, read from the token, not a person.
  identity='.jobs.batch.steps[] | select(.name == "Use the App'"'"'s bot as git identity and credential")'
  [ "$(yq -r "$identity | .env.APP_SLUG" "$WF")" = '${{ steps.app-token.outputs.app-slug }}' ]
  yq -r "$identity | .run" "$WF" | grep -qF 'gh api "/users/${APP_SLUG}[bot]" --jq .id'
  yq -r "$identity | .run" "$WF" | grep -qF 'user.email "${id}+${APP_SLUG}[bot]@users.noreply.github.com"'

  # No step reaches for the job's own token or a person's batch token.
  if grep -v '^[[:space:]]*#' "$WF" | grep -Eq 'secrets\.GITHUB_TOKEN|github\.token|secrets\.BACKPORT_BATCH_TOKEN|gh api user'; then
    echo "the workflow reads the job's own token or a person's"
    false
  fi
}
