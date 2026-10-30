#!/usr/bin/env bats
# The instructions Copilot code review reads for the pull request title check
# carry the list of docs/release/backport-tasks/title-check.md word for word,
# so the review and a maintainer running the task by hand check the same
# things.
#
# Run with: bats hack/copilot-instructions.bats

load test_helper

INSTRUCTIONS="${BATS_TEST_DIRNAME}/../.github/instructions/pr-title.instructions.md"
TASK="${BATS_TEST_DIRNAME}/../docs/release/backport-tasks/title-check.md"

# The lines under a "What to check" heading of any level, up to the next
# heading.
what_to_check() {
  awk '/^#+ What to check$/ { f = 1; next } f && /^#/ { exit } f' "$1"
}

@test "the review checks what the title task checks" {
  task=$(what_to_check "$TASK")
  [ -n "$task" ]
  [ "$(what_to_check "$INSTRUCTIONS")" = "$task" ]
}

@test "the instructions apply to every file and point at the task" {
  [ "$(sed -n '1p' "$INSTRUCTIONS")" = "---" ]
  [ "$(awk 'NR > 1 && /^---$/ { exit } NR > 1' "$INSTRUCTIONS" | grep -c '^applyTo: "\*\*"$')" -eq 1 ]
  grep -qF '(../../docs/release/backport-tasks/title-check.md)' "$INSTRUCTIONS"
}
