#!/usr/bin/env bats
# Tests for hack/retention-delete.sh — the delete half of the GHCR retention
# workflow. A refused DELETE must fail the run: a sweep that only warns stays
# green while deleting nothing, which is how retention went unnoticed before.
#
# Run with: bats hack/retention-delete_test.bats

load test_helper

SCRIPT="${BATS_TEST_DIRNAME}/retention-delete.sh"

# gh stub: records every call, refuses version 2 the way the API does.
_stub_gh() {
  mkdir -p "$1"
  cat > "$1/gh" <<'EOF'
#!/bin/sh
printf '%s token=%s\n' "$*" "${GH_TOKEN:-}" >> "$GH_CALLS"
case "$*" in
  */versions/2*) echo 'gh: Forbidden (HTTP 403)' >&2; exit 1 ;;
esac
EOF
  chmod +x "$1/gh"
}

setup() {
  strict_setup
  _stub_gh "${BATS_TEST_TMPDIR}/bin"
  export PATH="${BATS_TEST_TMPDIR}/bin:$PATH"
  export GH_CALLS="${BATS_TEST_TMPDIR}/calls"
  export ORG=cozystack
  export GH_TOKEN=app-token
  export DELETE_TOKEN=workflow-token
  : > "$GH_CALLS"
}

@test "apply: a refused delete is an error naming the HTTP status, the rest still run, exit is non-zero" {
  run env APPLY=true "$SCRIPT" cozystack/foo version 1 2 3
  [ "$status" -ne 0 ]
  [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 3 ]
  grep -q 'cozystack%2Ffoo/versions/3' "$GH_CALLS"
  case "$output" in
    *"::error::"*"version 2"*"HTTP 403"*) ;;
    *) echo "FAIL: no error with the HTTP status: $output"; false ;;
  esac
}

@test "apply: all deletes succeeding exits zero" {
  run env APPLY=true "$SCRIPT" cozystack/foo version 1 3
  [ "$status" -eq 0 ]
  [ "$(wc -l < "$GH_CALLS" | tr -d ' ')" -eq 2 ]
}

@test "dry-run calls nothing and exits zero" {
  run env APPLY=false "$SCRIPT" cozystack/foo version 1 2 3
  [ "$status" -eq 0 ]
  [ ! -s "$GH_CALLS" ]
  case "$output" in
    *"would delete cozystack/foo version 2"*) ;;
    *) echo "FAIL: dry-run did not report: $output"; false ;;
  esac
}

@test "apply: deletes with DELETE_TOKEN, not the token that listed the versions" {
  run env APPLY=true "$SCRIPT" cozystack/foo version 1
  [ "$status" -eq 0 ]
  grep -q 'versions/1 token=workflow-token$' "$GH_CALLS"
}

@test "apply: no DELETE_TOKEN is refused before any call" {
  run env -u DELETE_TOKEN APPLY=true "$SCRIPT" cozystack/foo version 1
  [ "$status" -ne 0 ]
  [ ! -s "$GH_CALLS" ]
  case "$output" in
    *"DELETE_TOKEN is required"*) ;;
    *) echo "FAIL: refusal does not name DELETE_TOKEN: $output"; false ;;
  esac
}

@test "dry-run needs no DELETE_TOKEN" {
  run env -u DELETE_TOKEN APPLY=false "$SCRIPT" cozystack/foo version 1
  [ "$status" -eq 0 ]
}
