#!/usr/bin/env bats

# Contract for the two label-writing workflows: pr-labeler.yaml (which labels
# it may apply and from what input) and stale.yaml (which labels drive the
# auto-close policy and who is exempt). Every label name either workflow can
# write or match must resolve against .github/labels.yml — a mistyped name is
# not an error at runtime, it is a rule that silently stops matching.
#
# These tests intentionally pin executable/structural lines, not prose: a
# mapping demoted to a YAML `#` or JavaScript `//` comment must never satisfy
# the contract. See hack/promote-gate-contract.bats for the pattern.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
PR_LABELER="$REPO_ROOT/.github/workflows/pr-labeler.yaml"
STALE="$REPO_ROOT/.github/workflows/stale.yaml"
LABELS="$REPO_ROOT/.github/labels.yml"

# Comment-stripping filters, one per language in these files. grep exits 1 on
# an empty selection (legitimate for an empty block) and 2 on a real error, so
# only the latter propagates.
code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

script_lines() {
  local rc=0
  grep -v '^[[:space:]]*//' || rc=$?
  [ "$rc" -le 1 ]
}

# Body of a `const <name> = {` ... `};` object literal inside the
# github-script block, comment lines already stripped.
js_object_block() {
  awk -v name="const $1 = {" '
    index($0, name) { inside = 1; next }
    inside && /^[[:space:]]*};[[:space:]]*$/ { exit }
    inside' "$2" | script_lines
}

# Every label defined in .github/labels.yml, one per line.
defined_labels() {
  code_lines < "$LABELS" | awk '/^- name: / { print $3 }'
}

# `key: value` inputs of the stale action, comment lines stripped.
stale_input() {
  code_lines < "$STALE" | awk -v key="          $1: " '
    index($0, key) == 1 { sub(key, ""); print; exit }'
}

# A `>-` folded list input of the stale action, flattened to one label per line.
stale_exempt_list() {
  code_lines < "$STALE" | awk -v key="          $1: >-" '
    $0 == key { inside = 1; next }
    inside && !/^            / { exit }
    inside { print }' | tr -d ' ' | tr ',' '\n' | grep -v '^$'
}

assert_labels_defined() {
  # $1: newline-separated label names. Fails naming the missing ones.
  # POSIX-only on purpose: hack/cozytest.sh runs these files in plain sh.
  local all missing
  all="$(defined_labels)"
  missing="$(printf '%s\n' "$1" | sort -u | while read -r l; do
    [ -n "$l" ] || continue
    printf '%s\n' "$all" | grep -qxF "$l" || printf '%s\n' "$l"
  done)"
  if [ -n "$missing" ]; then
    echo "labels not defined in .github/labels.yml: $missing" >&2
    return 1
  fi
}

@test "every label pr-labeler can apply resolves against labels.yml" {
  kind_labels="$(js_object_block typeToKind "$PR_LABELER" | grep -o "'kind/[^']*'" | tr -d "'")"
  [ -n "$kind_labels" ]
  assert_labels_defined "$kind_labels"

  area_labels="$(js_object_block scopeToArea "$PR_LABELER" | grep -o "'area/[^']*'" | tr -d "'")"
  [ -n "$area_labels" ]
  assert_labels_defined "$area_labels"

  # Labels added outside the two tables: backport prefix, breaking-change
  # footer, and the no-area fallback.
  literal_labels="$(code_lines < "$PR_LABELER" | script_lines \
    | grep -o "toAdd.add('[^']*')" | sed "s/toAdd.add('//; s/')//" \
    | grep -v '^typeToKind\|^scopeToArea' || true)"
  for l in area/release kind/breaking-change area/uncategorized; do
    printf '%s\n' "$literal_labels" | grep -qxF "$l"
  done
  assert_labels_defined "$literal_labels"
}

@test "pr-labeler applies exactly the documented type mappings" {
  block="$(js_object_block typeToKind "$PR_LABELER")"
  count="$(printf '%s\n' "$block" | grep -c ":" || true)"
  [ "${count:-0}" -eq 5 ]
  for t in feat fix docs chore refactor; do
    printf '%s\n' "$block" | grep -qE "^[[:space:]]*$t:"
  done
  # Commented-out types must not count as mappings.
  for t in style perf test build ci revert; do
    ! printf '%s\n' "$block" | grep -qE "^[[:space:]]*$t:"
  done
}

@test "pr-labeler subscribes to opened/reopened/synchronize and not edited" {
  count="$(code_lines < "$PR_LABELER" | grep -cF '    types: [opened, reopened, synchronize]' || true)"
  [ "${count:-0}" -eq 1 ]
  # 'edited' is deliberately omitted (see the comment above the trigger); it
  # must not creep back in as an executable line.
  ! code_lines < "$PR_LABELER" | grep -E '^[[:space:]]*types:' | grep -q 'edited'
}

@test "pr-labeler is additive only and falls back to area/uncategorized" {
  lines="$(code_lines < "$PR_LABELER" | script_lines)"

  count="$(printf '%s\n' "$lines" | grep -cF "toAdd.add('area/uncategorized')" || true)"
  [ "${count:-0}" -eq 1 ]

  # Only labels not already present are sent, and only via addLabels: a
  # labeler that starts removing or replacing labels is a different tool.
  count="$(printf '%s\n' "$lines" | grep -cF '!existing.has(l)' || true)"
  [ "${count:-0}" -eq 1 ]
  count="$(printf '%s\n' "$lines" | grep -cF 'addLabels' || true)"
  [ "${count:-0}" -eq 1 ]
  ! printf '%s\n' "$lines" | grep -q 'removeLabel\|setLabels'
}

@test "every label stale.yaml writes or matches resolves against labels.yml" {
  for key in stale-issue-label stale-pr-label close-issue-label close-pr-label; do
    value="$(stale_input "$key")"
    [ -n "$value" ]
    assert_labels_defined "$value"
  done
  assert_labels_defined "$(stale_exempt_list exempt-issue-labels)"
  assert_labels_defined "$(stale_exempt_list exempt-pr-labels)"
}

@test "stale policy pins: label choices, exempt list sizes, day counts, write cap" {
  [ "$(stale_input stale-issue-label)" = "lifecycle/stale" ]
  [ "$(stale_input stale-pr-label)" = "lifecycle/stale" ]
  [ "$(stale_input close-issue-label)" = "lifecycle/rotten" ]
  [ "$(stale_input close-pr-label)" = "lifecycle/rotten" ]

  count="$(stale_exempt_list exempt-issue-labels | wc -l | tr -d ' ')"
  [ "$count" -eq 12 ]
  count="$(stale_exempt_list exempt-pr-labels | wc -l | tr -d ' ')"
  [ "$count" -eq 4 ]

  # The renewal path: frozen exempts both; an accepted issue or a held PR is
  # deliberate, not stale.
  stale_exempt_list exempt-issue-labels | grep -qxF 'lifecycle/frozen'
  stale_exempt_list exempt-issue-labels | grep -qxF 'triage/accepted'
  stale_exempt_list exempt-pr-labels | grep -qxF 'lifecycle/frozen'
  stale_exempt_list exempt-pr-labels | grep -qxF 'do-not-merge/hold'

  [ "$(stale_input days-before-stale)" = "60" ]
  [ "$(stale_input days-before-close)" = "14" ]
  [ "$(stale_input operations-per-run)" = "100" ]
  [ "$(stale_input remove-stale-when-updated)" = "true" ]
}
