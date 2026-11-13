#!/usr/bin/env bats
# Unit tests for hack/backport-batch-tests-needed.sh, which decides whether a
# pull request runs `make test-backport-batch`, and for its wiring into the
# checks job of pull-requests.yaml.
#
# Test-level EXIT traps replace Bats' own handler and hide failing TAP results.
# Cleanup follows aborting assertions; see docs/agents/e2e-testing.md.

load test_helper

CLASSIFIER="hack/backport-batch-tests-needed.sh"
WF=".github/workflows/pull-requests.yaml"

classify() {
  list="$1"
  shift
  printf '%s\n' "$@" > "$list"
  "$CLASSIFIER" "$list"
}

@test "a change to the tool, an imported package, a page its tests read or the module runs the tests" {
  tmp=$(mktemp)
  [ "$(classify "$tmp" cmd/backport-batch/report_test.go)" = true ]
  [ "$(classify "$tmp" internal/apigate/surface.go)" = true ]
  [ "$(classify "$tmp" pkg/config/config.go)" = true ]
  [ "$(classify "$tmp" docs/release/backports.md)" = true ]
  [ "$(classify "$tmp" docs/release/backport-tasks/conflict.md)" = true ]
  [ "$(classify "$tmp" go.sum)" = true ]
  # One input among unrelated paths is enough.
  [ "$(classify "$tmp" packages/apps/mariadb/values.yaml pkg/config/config.go)" = true ]
  rm -f "$tmp"
}

@test "a change that reaches none of its inputs does not run the tests" {
  tmp=$(mktemp)
  [ "$(classify "$tmp" packages/apps/mariadb/values.yaml)" = false ]
  [ "$(classify "$tmp" cmd/api-gate/main.go)" = false ]
  [ "$(classify "$tmp" hack/backport-batch-scheduled.sh)" = false ]
  [ "$(classify "$tmp" docs/release.md docs/release/other.md)" = false ]
  # A file entry is not a prefix: backports.md does not cover backports.md.bak,
  # and a directory entry does not cover a sibling sharing its name as prefix.
  [ "$(classify "$tmp" docs/release/backports.md.bak)" = false ]
  [ "$(classify "$tmp" pkg/configx/x.go)" = false ]
  [ "$(classify "$tmp" internal/apigate-old/x.go)" = false ]
  rm -f "$tmp"
}

@test "entries are trimmed as the Go test trims them" {
  tmp=$(mktemp -d)
  printf '%s\n' cmd/backport-batch/main.go > "$tmp/diff"
  printf '  cmd/backport-batch/ \r\n' > "$tmp/inputs"
  [ "$("$CLASSIFIER" "$tmp/diff" "$tmp/inputs")" = true ]
  rm -rf "$tmp"
}

@test "an inputs file with no entry is refused rather than read as never" {
  tmp=$(mktemp -d)
  printf '%s\n' cmd/backport-batch/main.go > "$tmp/diff"
  printf '# only a comment\n\n' > "$tmp/inputs"
  if "$CLASSIFIER" "$tmp/diff" "$tmp/inputs" >"$tmp/out" 2>"$tmp/err"; then
    echo "an empty inputs file must fail, it printed: $(cat "$tmp/out")" >&2
    exit 1
  fi
  grep -q 'lists no path' "$tmp/err"
  rm -rf "$tmp"
}

@test "the checks job runs the target on the classifier's answer alone" {
  plan_run=$(yq -r '.jobs.plan.steps[] | select(.id == "p") | .run' "$WF")
  printf '%s\n' "$plan_run" | grep -qF 'backport_batch="$(hack/backport-batch-tests-needed.sh /tmp/changed_norenames.txt)"'
  [ "$(yq -r '.jobs.plan.outputs.backport_batch' "$WF")" = '${{ steps.p.outputs.backport_batch }}' ]

  step_if=$(yq -r '.jobs.checks.steps[] | select(.name == "Run backport-batch tests") | .if' "$WF")
  step_run=$(yq -r '.jobs.checks.steps[] | select(.name == "Run backport-batch tests") | .run' "$WF")
  # !cancelled(): a failed unit lane above must not skip this suite too.
  [ "$step_if" = "\${{ !cancelled() && needs.plan.outputs.backport_batch == 'true' }}" ]
  [ "$step_run" = 'make test-backport-batch' ]

  # And nowhere else: the unit lane every code PR runs must not carry it.
  prereqs=$(sed -n 's/^unit-tests:[[:space:]]*//p' Makefile)
  [ -n "$prereqs" ]
  case " $prereqs " in
    *" test-backport-batch "*)
      echo "make unit-tests still runs test-backport-batch on every code PR: $prereqs" >&2
      exit 1 ;;
  esac
}
