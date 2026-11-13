#!/usr/bin/env bats
# Contract for the backport-batch-verify job of .github/workflows/pr-labeler.yaml,
# which runs `backport-batch verify` on every push to a batch pull request.
#
# The job runs under pull_request_target, so it holds a token on a pull
# request's events and must never run code from the pull request. These tests
# pin what keeps it that way: the tool is built from main, the head is only
# fetched, the token reads contents and nothing more, and no expression
# reaches a shell.
#
# Run with: bats hack/backport-batch-verify-job.bats

load test_helper

WF="${BATS_TEST_DIRNAME}/../.github/workflows/pr-labeler.yaml"
JOB='.jobs.backport-batch-verify'

@test "the job runs on pull_request_target, so main's copy reaches release lines" {
  [ "$(yq -r '.on | keys | .[]' "$WF")" = "pull_request_target" ]
  [ "$(yq -r "$JOB.name" "$WF")" = "Backport batch shape" ]
}

@test "the job runs only for a batch branch of this repository into a release line" {
  cond="$(yq -r "$JOB.if" "$WF")"
  [[ "$cond" == *"startsWith(github.event.pull_request.base.ref, 'release-')"* ]]
  [[ "$cond" == *"startsWith(github.event.pull_request.head.ref, 'backport-batch/')"* ]]
  [[ "$cond" == *"github.event.pull_request.head.repo.full_name == github.repository"* ]]
  [ "$(grep -o '&&' <<<"$cond" | wc -l)" -eq 2 ]
}

@test "the token reads contents and nothing else" {
  [ "$(yq -o=json -I=0 "$JOB.permissions" "$WF")" = '{"contents":"read"}' ]
}

@test "main is checked out, without credentials, and the head only fetched" {
  [ "$(yq -r "$JOB.steps[0].uses" "$WF")" = "actions/checkout@11d5960a326750d5838078e36cf38b85af677262" ]
  [ "$(yq -r "$JOB.steps[0].with.ref" "$WF")" = "main" ]
  [ "$(yq -r "$JOB.steps[0].with.persist-credentials" "$WF")" = "false" ]
  [ "$(yq -r "[$JOB.steps[] | select(.uses == \"actions/checkout*\")] | length" "$WF")" -eq 1 ]
  [ "$(yq -r "$JOB.steps[] | select(.name == \"Build backport-batch from main\") | .run" "$WF")" = 'go build -o "$RUNNER_TEMP/backport-batch" ./cmd/backport-batch' ]
  [ "$(yq -r "$JOB.steps[] | select(.name == \"Fetch the batch head as data\") | .run" "$WF")" = 'git fetch --no-tags --quiet origin "$HEAD_SHA"' ]
}

@test "the tool is built before the head is fetched, and verify gets it through env" {
  names="$(yq -r "$JOB.steps[].name" "$WF")"
  [ "$(grep -n 'Build backport-batch from main' <<<"$names" | cut -d: -f1)" -lt "$(grep -n 'Fetch the batch head as data' <<<"$names" | cut -d: -f1)" ]
  run_line="$(yq -r "$JOB.steps[] | select(.name == \"Verify every original on the batch\") | .run" "$WF")"
  [ "$run_line" = '"$RUNNER_TEMP/backport-batch" verify --line "$BASE_REF" --head "$HEAD_SHA"' ]
}

@test "no expression reaches a shell in the job" {
  runs="$(yq -r "$JOB.steps[] | select(has(\"run\")) | .run" "$WF")"
  [ -n "$runs" ]
  if grep -q '\${{' <<<"$runs"; then
    echo "a run: line carries an expression:"
    echo "$runs"
    false
  fi
}
