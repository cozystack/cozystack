#!/usr/bin/env bats
# promote-rc.yaml is dispatched from main only: a dispatch from another ref
# runs that ref's copy of the workflow, and the release signature names main
# as the identity. The refuse step has to be the first thing parse does, so a
# wrong dispatch fails before the staging branch, the promotion-* candidate or
# the draft release exist.
#
# Run with: bats hack/promote-rc-dispatch-ref_test.bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WF="$REPO_ROOT/.github/workflows/promote-rc.yaml"
STEP_NAME="Refuse a dispatch from any ref but main"

_step_run() {
  yq ".jobs.parse.steps[] | select(.name == \"$STEP_NAME\").run" "$WF"
}

@test "the refuse step is the first step of parse and reads github.ref" {
  [ "$(yq '.jobs.parse.steps[0].name' "$WF")" = "$STEP_NAME" ]
  [ "$(yq '.jobs.parse.steps[0].env.REF' "$WF")" = '${{ github.ref }}' ]
}

@test "a dispatch from main passes" {
  step="$(_step_run)"
  [ -n "$step" ]
  run env REF=refs/heads/main dash -c "$step"
  [ "$status" -eq 0 ]
}

@test "a dispatch from a release branch or a tag fails and names main" {
  step="$(_step_run)"
  for ref in refs/heads/release-1.7 refs/tags/v1.7.0-rc.3 ""; do
    run env REF="$ref" dash -c "$step"
    [ "$status" -ne 0 ]
    printf '%s\n' "$output" | grep -qF "::error::"
    printf '%s\n' "$output" | grep -qF "from 'main'"
  done
}
