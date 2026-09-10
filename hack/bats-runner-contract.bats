#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Hermetic contracts for the root BATS unit-test lane.
#
# The runner crosses five surfaces: the Makefile owns discovery and local
# execution, pull-requests.yaml provisions the CI toolchain, the pre-commit
# config decides when contributors run it, and docs/agents/overview.md tells
# contributors what to install. A partial edit leaves a lane that is green on
# one machine and absent or weaker on another, so these assertions hold the
# shared terms together.
#
# Run with: bats hack/bats-runner-contract.bats
# -----------------------------------------------------------------------------

load test_helper

BRC_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
BRC_REPO_ROOT="$(cd "$BRC_DIR/.." && pwd)"
BRC_WORKFLOW="$BRC_REPO_ROOT/.github/workflows/pull-requests.yaml"
BRC_PRECOMMIT_WORKFLOW="$BRC_REPO_ROOT/.github/workflows/pre-commit.yml"

brc_toolchain_script() {
  yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .run' "$BRC_WORKFLOW"
  return 0
}

brc_write_parallel_stub() {
  printf '%s\n' '#!/bin/sh' "printf '%s\\n' '$2'" > "$1/parallel"
  chmod +x "$1/parallel"
  return 0
}

@test "CI installs the exact commit behind the Bats release tag" {
  version=$(yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.BATS_VERSION' "$BRC_WORKFLOW")
  commit=$(yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.BATS_COMMIT' "$BRC_WORKFLOW")
  [ "$version" = "1.14.0" ]
  [ "$commit" = "eb7f42f8d608ac693d7a4b67474f6714ea68cfc5" ]

  script=$(brc_toolchain_script)
  printf '%s\n' "$script" | grep -Fq 'git clone --quiet --depth=1 --branch "v$BATS_VERSION"'
  printf '%s\n' "$script" | grep -Fq 'test "$(git -C "$bats_source" rev-parse HEAD)" = "$BATS_COMMIT"'
  gates=$(printf '%s\n' "$script" | grep -Fc 'grep -qx "Bats $BATS_VERSION"' || :)
  [ "$gates" -eq 2 ]
  if printf '%s\n' "$script" | grep -q '/archive/'; then
    echo "FAIL: the Bats installer pins a byte-unstable generated archive"
    false
  fi
}

@test "the job-count fallback accepts GNU Parallel only" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin"
  brc_write_parallel_stub "$tmp/bin" "parallel from moreutils"
  printf '%s\n' '#!/bin/sh' 'printf "7\\n"' > "$tmp/bin/nproc"
  chmod +x "$tmp/bin/nproc"

  jobs=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= PATH="$tmp/bin:$PATH" make --no-print-directory -s print-bats-jobs)
  [ "$jobs" = "1" ]

  brc_write_parallel_stub "$tmp/bin" "GNU parallel 20260722"
  jobs=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= PATH="$tmp/bin:$PATH" make --no-print-directory -s print-bats-jobs)
  [ "$jobs" = "7" ]
  rm -rf "$tmp"
}

@test "the complete local hook is manual and documented" {
  hook_entry=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .entry' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  hook_stages=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .stages | join(",")' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  hook_passes_files=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .pass_filenames' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  [ "$hook_entry" = 'make bats-unit-tests bats-posix-compat-tests' ]
  [ "$hook_stages" = 'manual' ]
  [ "$hook_passes_files" = 'false' ]
  grep -Fq 'bats-core 1.5 or newer' "$BRC_REPO_ROOT/docs/agents/overview.md"
  grep -Fq 'pre-commit run bats-unit-tests --hook-stage manual --all-files' "$BRC_REPO_ROOT/docs/agents/overview.md"
}

@test "pull-request CI keeps the required Bats lane unconditional" {
  lint_skip=$(yq -r '.jobs.pre-commit.steps[] | select(.name == "Run pre-commit hooks") | .env.SKIP // ""' "$BRC_PRECOMMIT_WORKFLOW")
  code_command=$(yq -r '.jobs.checks.steps[] | select(.name == "Run unit tests") | .run' "$BRC_WORKFLOW")
  docs_command=$(yq -r '.jobs.checks.steps[] | select(.name == "Run Bats unit tests for docs-only changes") | .run' "$BRC_WORKFLOW")
  [ -z "$lint_skip" ]
  [ "$code_command" = 'make unit-tests' ]
  [ "$docs_command" = 'make bats-unit-tests' ]
}

@test "the POSIX compatibility lane retains reviewed and sourced shell-facing files" {
  compat_files=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory -s print-bats-posix-compat-files)
  unit_files=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory -s print-bats-unit-files)
  sourced_shell_tests=$(cd "$BRC_REPO_ROOT" && grep -El '^[[:space:]]*(\.|source)[[:space:]]+.*\.sh' $unit_files)
  [ -n "$sourced_shell_tests" ]
  for file in \
    $sourced_shell_tests \
    hack/capture-dataplane.bats \
    hack/capture-previous-logs.bats \
    hack/cilium-leak-healer_test.bats \
    hack/cozyreport-talos.bats \
    hack/cozyreport.bats \
    hack/cozystack-version-stamp.bats \
    hack/nightly-mirror_test.bats \
    hack/pod-label-census_test.bats \
    hack/promote-rewrite-tags_test.bats \
    hack/runner-identity.bats \
    hack/seaweedfs-naming-audit.bats; do
    printf '%s\n' "$compat_files" | grep -Fxq "$file"
  done
}

@test "a new non-Chainsaw shell dependency joins the compatibility lane" {
  tmp=$(mktemp -d)
  fixture="$tmp/non-chainsaw-helper.bats"
  printf '%s\n' '#!/usr/bin/env bats' '. hack/lib/image-refs.sh' > "$fixture"
  make_status=0
  compat_files=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory -s "BATS_UNIT_FILES=$fixture" print-bats-posix-compat-files) || make_status=$?
  rm -rf "$tmp"

  [ "$make_status" -eq 0 ]
  printf '%s\n' "$compat_files" | grep -Fxq "$fixture"
}

@test "a failed compatibility file does not suppress later files" {
  tmp=$(mktemp -d)
  first="$tmp/first.bats"
  second="$tmp/second.bats"
  marker="$tmp/later-ran"
  printf '%s\n' '@test "first fails" {' '  false' '}' > "$first"
  printf '%s\n' '@test "later runs" {' "  : > '$marker'" '}' > "$second"

  make_status=0
  output=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory BATS_POSIX_SHELL=dash "BATS_POSIX_COMPAT_FILES=$first $second" bats-posix-compat-tests 2>&1) || make_status=$?
  marker_present=0
  [ -e "$marker" ] && marker_present=1
  rm -rf "$tmp"

  [ "$make_status" -ne 0 ]
  [ "$marker_present" -eq 1 ]
  printf '%s\n' "$output" | grep -Fq -- "--- running POSIX compatibility: $first ---"
  printf '%s\n' "$output" | grep -Fq -- "--- running POSIX compatibility: $second ---"
}
