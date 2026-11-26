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

# `on` may be a single event name, a list of names or a map keyed by name.
events() {
  yq '.on | [(select(tag == "!!str")), (select(tag == "!!seq") | .[]), (select(tag == "!!map") | keys | .[])] | .[]' "$1"
}

# A workflow yq cannot parse fails the audit: dropping it would leave its jobs
# unchecked while every other workflow passes.
guarded_workflows() {
  local f names rc=0
  for f in "$1"/*.yml "$1"/*.yaml; do
    [ -e "$f" ] || continue
    if ! names="$(events "$f")"; then
      echo "cannot parse $f" >&2
      rc=1
      continue
    fi
    if grep -qxE 'push|schedule' <<<"$names"; then
      printf '%s\n' "$f"
    fi
  done
  return "$rc"
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

# True when the whole string is one parenthesized group: the parenthesis that
# opens it closes on the last character. Parentheses inside single-quoted
# literals do not count.
one_group() {
  local s="$1" depth=0 quoted=0 i c
  [ "${s:0:1}" = "(" ] || return 1
  for ((i = 0; i < ${#s}; i++)); do
    c="${s:i:1}"
    if [ "$c" = "'" ]; then
      quoted=$((1 - quoted))
    elif [ "$quoted" -eq 0 ] && [ "$c" = "(" ]; then
      depth=$((depth + 1))
    elif [ "$quoted" -eq 0 ] && [ "$c" = ")" ]; then
      depth=$((depth - 1))
      [ "$depth" -eq 0 ] && [ "$i" -ne $((${#s} - 1)) ] && return 1
    fi
  done
  [ "$depth" -eq 0 ] && [ "$quoted" -eq 0 ]
}

# The guard must be the leading conjunct, alone or ANDed with the job's own
# condition as one parenthesized group. `&&` binds tighter than `||`, so a
# tail like `(a) || (b)` would let `b` reopen the job.
is_guarded() {
  local cond="$1" guard="$2"
  [ "$cond" = "$guard" ] && return 0
  case "$cond" in
    "$guard && "*) one_group "${cond#"$guard && "}" ;;
    *) return 1 ;;
  esac
}

unguarded_jobs() {
  local f="$1" guard="$GUARD" job cond
  if events "$f" | grep -qx pull_request; then
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
  local f list missing
  list="$(guarded_workflows "$WORKFLOWS")"
  missing="$(while IFS= read -r f; do unguarded_jobs "$f"; done <<<"$list")"
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
  if is_guarded "$GUARD && (true) || (github.event_name == 'push')" "$GUARD"; then false; fi
  if is_guarded "$PR_GUARD && (true) || (github.event_name == 'push')" "$PR_GUARD"; then false; fi
  if is_guarded "$GUARD && (')' == ')') || (true)" "$GUARD"; then false; fi
  is_guarded "$GUARD && (contains(github.ref_name, ')'))" "$GUARD"
}

@test "an on given as a string or a list is read as event names" {
  local dir
  dir="$(mktemp -d)"
  printf 'on: push\njobs: {}\n' > "$dir/string.yaml"
  printf 'on: [pull_request, schedule]\njobs: {}\n' > "$dir/list.yaml"
  printf 'on: [pull_request]\njobs: {}\n' > "$dir/pr-only.yaml"
  run guarded_workflows "$dir"
  rm -rf "$dir"
  [ "$status" -eq 0 ]
  [ "$output" = "$(printf '%s\n' "$dir/list.yaml" "$dir/string.yaml")" ]
}

@test "an unparseable workflow fails the audit instead of dropping out of it" {
  local dir
  dir="$(mktemp -d)"
  printf 'on: [push\njobs: {}\n' > "$dir/broken.yaml"
  printf 'on: push\njobs: {}\n' > "$dir/fine.yaml"
  run guarded_workflows "$dir"
  rm -rf "$dir"
  [ "$status" -ne 0 ]
  [[ "$output" == *"broken.yaml"* ]]
}

@test "a list on that names pull_request requires the pull request form" {
  local dir
  dir="$(mktemp -d)"
  cat > "$dir/both.yaml" <<'EOF'
on: [push, pull_request]
jobs:
  plain:
    if: github.repository == 'cozystack/cozystack'
    runs-on: ubuntu-latest
    steps: [{run: "true"}]
EOF
  run unguarded_jobs "$dir/both.yaml"
  rm -rf "$dir"
  [ "$status" -eq 0 ]
  [ "$output" = "both.yaml:plain" ]
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
