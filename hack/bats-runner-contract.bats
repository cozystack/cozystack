#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Static and hermetic contracts for the root BATS unit-test lane.
#
# The runner crosses four files: the Makefile owns discovery and local
# execution, pull-requests.yaml provisions the CI toolchain, pre-commit decides
# when contributors run it, and docs/agents/overview.md tells them what to
# install. A partial edit leaves a lane that is green on one machine and absent
# or weaker on another, so these assertions hold the shared terms together.
#
# Run with: bats hack/bats-runner-contract.bats
# -----------------------------------------------------------------------------

load test_helper

BRC_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
BRC_REPO_ROOT="$(cd "$BRC_DIR/.." && pwd)"
BRC_WORKFLOW="$BRC_REPO_ROOT/.github/workflows/pull-requests.yaml"

brc_toolchain_script() {
  yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .run' "$BRC_WORKFLOW"
  return 0
}

brc_write_parallel_stub() {
  printf '%s\n' '#!/bin/sh' "printf '%s\\n' '$2'" > "$1/parallel"
  chmod +x "$1/parallel"
  return 0
}

@test "CI installs the exact checksum-verified Bats commit" {
  version=$(yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.BATS_VERSION' "$BRC_WORKFLOW")
  commit=$(yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.BATS_COMMIT' "$BRC_WORKFLOW")
  digest=$(yq -r '.jobs.checks.steps[] | select(.name == "Set up test toolchain") | .env.BATS_SHA256' "$BRC_WORKFLOW")
  [ "$version" = "1.14.0" ]
  [ "$commit" = "eb7f42f8d608ac693d7a4b67474f6714ea68cfc5" ]
  [ "$digest" = "845574549f4c9777bf02fcdf307f1bf347d40c66920fb6b47dcc8fdfa065ac39" ]

  script=$(brc_toolchain_script)
  printf '%s\n' "$script" | grep -Fq 'https://github.com/bats-core/bats-core/archive/$BATS_COMMIT.tar.gz'
  printf '%s\n' "$script" | grep -Fq 'sha256sum -c -'
  gates=$(printf '%s\n' "$script" | grep -Fc 'grep -qx "Bats $BATS_VERSION"' || :)
  [ "$gates" -eq 2 ]
  if printf '%s\n' "$script" | grep -q 'git clone'; then
    echo "FAIL: the Bats installer still trusts a movable git ref"
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

@test "the local hook covers the tree and documents its prerequisite" {
  hook_filter=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .files' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  hook_entry=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .entry' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  hook_always_run=$(yq -r '.repos[].hooks[] | select(.id == "bats-unit-tests") | .always_run' "$BRC_REPO_ROOT/.pre-commit-config.yaml")
  [ "$hook_filter" = '^.*$' ]
  [ "$hook_entry" = 'make bats-unit-tests bats-posix-compat-tests' ]
  [ "$hook_always_run" = 'true' ]
  grep -Fq 'bats-core 1.5 or newer' "$BRC_REPO_ROOT/docs/agents/overview.md"
  grep -Fq 'SKIP=bats-unit-tests git commit' "$BRC_REPO_ROOT/docs/agents/overview.md"
}

@test "the POSIX compatibility lane retains every reviewed shell-facing file" {
  recipe=$(cd "$BRC_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory -n bats-posix-compat-tests)
  for file in \
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
    printf '%s\n' "$recipe" | grep -Fq "$file"
  done
}
