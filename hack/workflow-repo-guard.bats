#!/usr/bin/env bats

# Every job of a workflow that a push or a schedule can start runs only in
# cozystack/cozystack. A private copy of the repository (the embargo mirror
# docs/security/incident-response.md describes) receives these files with every
# sync, and without the guard each sync and each cron tick there would build,
# publish, prune and release with that copy's secrets. Pull request events stay
# allowed, because checking a pull request is what such a copy exists for.
#
# Every job is pinned rather than only the roots: a job whose `if` calls a
# status function such as `!cancelled()` runs even when the job it needs was
# skipped, so guarding the roots alone does not stop it.

load test_helper

ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
WORKFLOWS="$ROOT/.github/workflows"

GUARD="github.repository == 'cozystack/cozystack'"
PR_GUARD="(github.event_name == 'pull_request' || $GUARD)"

guarded_workflows() {
  local f
  for f in "$1"/*.yml "$1"/*.yaml; do
    [ -e "$f" ] || continue
    if [ "$(yq '(.on | has("push")) or (.on | has("schedule"))' "$f")" = true ]; then
      printf '%s\n' "$f"
    fi
  done
}

# `${{ }}` and line breaks do not change what an `if` means, so both are
# dropped before the condition is compared with the accepted forms.
normalize() {
  tr '\n' ' ' | sed -E \
    -e 's/^[[:space:]]*\$\{\{[[:space:]]*//' \
    -e 's/[[:space:]]*\}\}[[:space:]]*$//' \
    -e 's/[[:space:]]+/ /g' \
    -e 's/^ //' -e 's/ $//'
}

# The guard must be the leading conjunct, alone or ANDed with the job's own
# condition in parentheses, so nothing ORed after it can reopen the job.
is_guarded() {
  local cond="$1" guard="$2"
  case "$cond" in
    "$guard") return 0 ;;
    "$guard && ("*")") return 0 ;;
  esac
  return 1
}

unguarded_jobs() {
  local f="$1" guard="$GUARD" job cond
  if [ "$(yq '.on | has("pull_request")' "$f")" = true ]; then
    guard="$PR_GUARD"
  fi
  while IFS= read -r job; do
    cond="$(yq ".jobs[\"$job\"].if // \"\"" "$f" | normalize)"
    is_guarded "$cond" "$guard" || printf '%s:%s\n' "$(basename "$f")" "$job"
  done < <(yq '.jobs | keys | .[]' "$f")
}

@test "workflows a push or a schedule starts are discovered" {
  run guarded_workflows "$WORKFLOWS"
  [ "$status" -eq 0 ]
  [[ "$output" == *"/build-main.yaml"* ]]
  [[ "$output" == *"/nightly.yaml"* ]]
  [[ "$output" == *"/tags.yaml"* ]]
}

@test "every job of a push or schedule workflow runs only in cozystack/cozystack" {
  local f missing
  missing="$(while IFS= read -r f; do unguarded_jobs "$f"; done < <(guarded_workflows "$WORKFLOWS"))"
  if [ -n "$missing" ]; then
    echo "jobs without the repository guard:" >&2
    echo "$missing" >&2
    false
  fi
}

@test "guard check accepts the canonical forms" {
  is_guarded "$GUARD" "$GUARD"
  is_guarded "$GUARD && (contains(github.ref_name, '-'))" "$GUARD"
  is_guarded "$PR_GUARD" "$PR_GUARD"
  is_guarded "$PR_GUARD && (github.event_name != 'pull_request')" "$PR_GUARD"
  [ "$(printf '%s\n' "\${{ $GUARD &&" "  (!cancelled()) }}" | normalize)" = "$GUARD && (!cancelled())" ]
}

@test "guard check rejects a missing, trailing or reopened guard" {
  if is_guarded "" "$GUARD"; then false; fi
  if is_guarded "contains(github.ref_name, '-')" "$GUARD"; then false; fi
  if is_guarded "contains(github.ref_name, '-') && $GUARD" "$GUARD"; then false; fi
  if is_guarded "$GUARD || true" "$GUARD"; then false; fi
  if is_guarded "github.event_name == 'pull_request'" "$PR_GUARD"; then false; fi
  if is_guarded "$GUARD" "$PR_GUARD"; then false; fi
}

@test "an unguarded job in a scheduled workflow is reported" {
  local dir
  dir="$(mktemp -d)"
  cat > "$dir/cron.yaml" <<'EOF'
on:
  schedule:
    - cron: "0 0 * * *"
jobs:
  guarded:
    if: github.repository == 'cozystack/cozystack'
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
  open:
    needs: guarded
    if: ${{ !cancelled() }}
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
EOF
  run unguarded_jobs "$dir/cron.yaml"
  rm -rf "$dir"
  [ "$status" -eq 0 ]
  [ "$output" = "cron.yaml:open" ]
}
