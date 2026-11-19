#!/usr/bin/env bats
# Every discovered unit file must load the nounset helper once. A file-local
# setup() replaces the helper's setup, so its first command must restore it.
#
# This lexical audit accepts one spelling at column zero, not arbitrary shell:
# conditional loads are rejected, while a literal inside a heredoc could be
# mistaken for a load. Fixtures use printf to avoid that ambiguity. Nested Bats
# canaries separately verify the helper's effect in the actual test process.

load test_helper

# The directory to audit. Under the bats binary that is this file's own
# location; under cozytest.sh, which does not set BATS_TEST_FILENAME, `$0` is
# the runner itself -- and the answer comes out the same only because the runner
# lives in the directory it runs files from. The first test below refuses to
# pass on an empty enumeration, so a wrong answer here fails rather than audits
# nothing.
BSS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
BSS_REPO_ROOT="$(cd "$BSS_DIR/.." && pwd)"

# Non-recursive, and the exclusion is on the basename, because that is what
# `$(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))` in the Makefile means.
# A shell glob keeps this portable to the macOS hook instead of relying on
# GNU find's non-POSIX `-maxdepth` flag.
# Nested files are outside this unit enumeration. Runner reachability is
# checked separately by hack/bats-runner-coverage.bats.
bss_units() {
  for _f in "$1"/*.bats; do
    [ -e "$_f" ] || continue
    case ${_f##*/} in
      e2e-*) continue ;;
    esac
    printf '%s\n' "$_f"
  done | sort
  return 0
}

# Report every unit file whose strict-mode setup is missing or overridden, one
# line each, and print nothing when they all comply.
#
# Through stdout rather than an exit status: cozytest.sh rewrites a function's
# closing brace into `return 0`, so a status set in here would be discarded
# before the caller could read it, and the check would pass unconditionally.
bss_audit() {
  bss_units "$1" | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    _b=$(basename "$_f")
    _load_count=$(grep -c '^load test_helper$' "$_f" || :)
    if [ "$_load_count" -eq 0 ]; then
      echo "$_b: does not load the strict-mode helper; add a column-zero \`load test_helper\` line under the file's leading comment block"
      continue
    fi
    if [ "$_load_count" -ne 1 ]; then
      echo "$_b: loads the strict-mode helper $_load_count times; keep exactly one column-zero \`load test_helper\` line"
      continue
    fi
    # A file-local setup replaces the helper's without a word from either
    # runner. Recognize the common Bash forms, including indentation,
    # `function setup {`, and a brace on the following line, then require the
    # suite's one canonical spelling. That makes an ambiguous nested declaration
    # fail closed instead of guessing whether Bats will see it at source time.
    _setup_count=$(grep -cE '^[[:space:]]*(setup[[:space:]]*\(|function[[:space:]]+setup([[:space:]]|\(|[{]|$))' "$_f" || :)
    _canonical_count=$(grep -c '^setup() {$' "$_f" || :)
    if [ "$_setup_count" -gt 0 ] \
      && { [ "$_setup_count" -ne 1 ] || [ "$_canonical_count" -ne 1 ]; }; then
      echo "$_b: uses a noncanonical or repeated setup declaration; use exactly one column-zero \`setup() {\` block"
      continue
    fi
    if [ "$_canonical_count" -eq 1 ]; then
      _first_effective=$(awk '
        /^setup\(\) \{$/ { in_setup=1; next }
        in_setup && /^}$/ { exit }
        in_setup && /^[[:space:]]*$/ { next }
        in_setup && /^[[:space:]]*#/ { next }
        in_setup { print; exit }
      ' "$_f")
      if ! printf '%s\n' "$_first_effective" \
        | grep -qE '^[[:space:]]*strict_setup[[:space:]]*$'; then
        echo "$_b: setup() must call strict_setup directly as its first effective command; blank and comment lines may precede it"
      fi
    fi
  done
  return 0
}

# The load line is assembled from a printf argument rather than
# written as a literal, because this file is audited by the guard it defines and
# a column-zero literal here would be read as a second declaration of its own.
bss_new_fixture() {
  printf '%s\n' '#!/usr/bin/env bats' > "$1/subject.bats"
  return 0
}

bss_add_load() {
  printf '%s\n' 'load test_helper' >> "$1/subject.bats"
  return 0
}

bss_add_indented_load() {
  printf '  %s\n' 'load test_helper' >> "$1/subject.bats"
  return 0
}

bss_add_commented_load() {
  printf '# %s\n' 'load test_helper' >> "$1/subject.bats"
  return 0
}

bss_add_own_setup() {
  printf '%s\n' 'setup() {' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_strict_own_setup() {
  printf '%s\n' 'setup() {' '' '  # Restore the unit-suite contract first.' '  strict_setup' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_subshell_strict_own_setup() {
  printf '%s\n' 'setup() {' '  (' '    strict_setup' '  )' '}' >> "$1/subject.bats"
  return 0
}

bss_add_conditional_strict_own_setup() {
  printf '%s\n' 'setup() {' '  if false; then' '    strict_setup' '  fi' '}' >> "$1/subject.bats"
  return 0
}

bss_add_function_own_setup() {
  printf '%s\n' 'function setup {' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_indented_own_setup() {
  printf '%s\n' '  setup() {' '    export FIXTURE=1' '  }' >> "$1/subject.bats"
  return 0
}

bss_add_next_line_brace_setup() {
  printf '%s\n' 'setup()' '{' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_spaced_parens_setup() {
  printf '%s\n' 'setup ( ) {' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_commented_strict_own_setup() {
  printf '%s\n' 'setup() {' '  # strict_setup' '  export FIXTURE=1' '}' >> "$1/subject.bats"
  return 0
}

bss_add_unrelated_strict_call() {
  printf '%s\n' 'unrelated() {' '  strict_setup' '}' >> "$1/subject.bats"
  return 0
}

bss_rename() {
  mv "$1/subject.bats" "$1/$2"
  return 0
}

bss_add_nounset_canary() {
  printf '%s\n' \
    '@test "nounset canary" {' \
    '  printf "%s\n" "$BSS_NOUNSET_CANARY_UNSET"' \
    '}' >> "$1/subject.bats"
  ln -s "$BSS_DIR/test_helper.bash" "$1/test_helper.bash"
  return 0
}

@test "the unit enumeration is not empty" {
  count=$(bss_units "$BSS_DIR" | wc -l)
  if [ "$count" -lt 2 ]; then
    echo "FAIL: enumerated $count unit file(s) under $BSS_DIR; the audit below would be vacuous"
    false
  fi
}

@test "the unit enumeration is non-recursive without GNU find extensions" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/nested"
  : > "$tmp/root.bats"
  : > "$tmp/e2e-live.bats"
  : > "$tmp/nested/child.bats"
  printf '%s\n' '#!/bin/sh' 'exit 97' > "$tmp/bin/find"
  chmod +x "$tmp/bin/find"

  units=$(PATH="$tmp/bin:$PATH" bss_units "$tmp")
  [ "$units" = "$tmp/root.bats" ]
  rm -rf "$tmp"
}

@test "the audited set is exactly the set the Makefile hands the runner" {
  # Ask make for its own answer instead of trusting this file's copy of the
  # rule. The whole point of the audit is that its input tracks the runner's,
  # so a divergence between the two is the failure it exists to prevent --
  # narrow the Makefile's filter without narrowing this one and the audit goes
  # on reporting clean about files nobody runs, or vice versa.
  tmp=$(mktemp -d)
  bss_units "$BSS_DIR" | sed "s|^$BSS_REPO_ROOT/||" | sort > "$tmp/mine"
  # MAKEFLAGS cleared: this runs from inside `make bats-unit-tests`, and a
  # sub-make that inherits the parent's `-j` without its jobserver descriptors
  # warns about it. The warning goes to stderr and would not corrupt the
  # comparison, but it would land in the middle of a green suite's output.
  (cd "$BSS_REPO_ROOT" && MAKEFLAGS= MAKELEVEL= make --no-print-directory -s print-bats-unit-files) | sort > "$tmp/theirs"
  # File comparison keeps this audit compatible with cozytest's POSIX shell;
  # process substitution would require Bash.
  if ! diff -u "$tmp/theirs" "$tmp/mine" > "$tmp/delta"; then
    echo "FAIL: the audited set and \$(BATS_UNIT_FILES) disagree (-Makefile +audit):"
    cat "$tmp/delta"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "default and direct canonical setup enable nounset in real nested Bats tests" {
  tmp=$(mktemp -d)
  unset BSS_NOUNSET_CANARY_UNSET

  for form in default direct; do
    mkdir "$tmp/$form"
    bss_new_fixture "$tmp/$form"
    bss_add_load "$tmp/$form"
    if [ "$form" = direct ]; then
      bss_add_strict_own_setup "$tmp/$form"
    fi
    bss_add_nounset_canary "$tmp/$form"
    report=$(bss_audit "$tmp/$form")
    if [ -n "$report" ]; then
      echo "FAIL: the supported $form setup was rejected: $report"
      rm -rf "$tmp"
      false
    fi

    child_status=0
    output=$(bats --formatter tap "$tmp/$form/subject.bats" 2>&1) || child_status=$?
    if [ "$child_status" -eq 0 ]; then
      echo "FAIL: nested Bats accepted an unset test-body read with $form setup"
      echo "$output"
      rm -rf "$tmp"
      false
    fi
    printf '%s\n' "$output" | grep -Fq 'not ok 1 nounset canary'
    printf '%s\n' "$output" | grep -Fq 'BSS_NOUNSET_CANARY_UNSET: unbound variable'
  done
  rm -rf "$tmp"
}

@test "the helper leaves subprocess shell options unchanged" {
  tmp=$(mktemp -d)
  unset BSS_NOUNSET_CANARY_UNSET
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  ln -s "$BSS_DIR/test_helper.bash" "$tmp/test_helper.bash"
  printf '%s\n' '#!/bin/bash' 'printf "child:%s\n" "$BSS_NOUNSET_CANARY_UNSET"' > "$tmp/child.sh"
  printf '@test "subprocess canary" {\n  bash "%s/child.sh"\n}\n' "$tmp" >> "$tmp/subject.bats"
  child_status=0
  output=$(bats --formatter tap "$tmp/subject.bats" 2>&1) || child_status=$?
  [ "$child_status" -eq 0 ]
  printf '%s\n' "$output" | grep -Fq 'ok 1 subprocess canary'
  rm -rf "$tmp"
}

@test "every hack/*.bats unit file restores set -u through the shared helper" {
  report=$(bss_audit "$BSS_DIR")
  if [ -n "$report" ]; then
    echo "FAIL: strict-mode setup missing or overridden:"
    echo "$report"
    false
  fi
}

@test "a unit file with no load line is reported" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'does not load the strict-mode helper'; then
    echo "FAIL: an uncovered file was not reported; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a unit file with the load line is clean" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  report=$(bss_audit "$tmp")
  if [ -n "$report" ]; then
    echo "FAIL: a covered file was reported: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "duplicate load lines are reported" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_load "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'keep exactly one column-zero'; then
    echo "FAIL: duplicate load lines were accepted; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "an indented or commented-out load line does not count" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_indented_load "$tmp"
  bss_add_commented_load "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'does not load the strict-mode helper'; then
    echo "FAIL: a load line that never runs was credited; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a file-local setup that skips strict_setup is reported" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'first effective command'; then
    echo "FAIL: a setup() override was not reported; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a file-local setup that calls strict_setup is clean" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_strict_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if [ -n "$report" ]; then
    echo "FAIL: a compliant setup() override was reported: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "subshell and conditional strict_setup placements are rejected and leave nounset off" {
  tmp=$(mktemp -d)
  unset BSS_NOUNSET_CANARY_UNSET

  for form in subshell conditional; do
    mkdir "$tmp/$form"
    bss_new_fixture "$tmp/$form"
    bss_add_load "$tmp/$form"
    if [ "$form" = subshell ]; then
      bss_add_subshell_strict_own_setup "$tmp/$form"
    else
      bss_add_conditional_strict_own_setup "$tmp/$form"
    fi
    bss_add_nounset_canary "$tmp/$form"

    report=$(bss_audit "$tmp/$form")
    if ! printf '%s\n' "$report" | grep -q 'first effective command'; then
      echo "FAIL: the unsafe $form setup was accepted: $report"
      rm -rf "$tmp"
      false
    fi

    child_status=0
    output=$(bats --formatter tap "$tmp/$form/subject.bats" 2>&1) || child_status=$?
    if [ "$child_status" -ne 0 ]; then
      echo "FAIL: the $form fixture unexpectedly enabled nounset:"
      echo "$output"
      rm -rf "$tmp"
      false
    fi
    printf '%s\n' "$output" | grep -Fq 'ok 1 nounset canary'
  done
  rm -rf "$tmp"
}

@test "function-style or indented setup declarations are rejected" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_function_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'noncanonical or repeated setup declaration'; then
    echo "FAIL: function-style setup was not rejected; got: $report"
    rm -rf "$tmp"
    false
  fi
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_indented_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'noncanonical or repeated setup declaration'; then
    echo "FAIL: indented setup was not rejected; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a setup declaration with its brace on the next line is rejected" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_next_line_brace_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'noncanonical or repeated setup declaration'; then
    echo "FAIL: a brace-on-next-line setup was not rejected; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a setup declaration with spaced parentheses is rejected" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_spaced_parens_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'noncanonical or repeated setup declaration'; then
    echo "FAIL: a spaced-parentheses setup was not rejected; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a comment-only strict_setup mention does not satisfy setup" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_commented_strict_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'first effective command'; then
    echo "FAIL: a comment-only strict_setup mention was credited; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "a strict_setup call outside setup does not satisfy setup" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_add_load "$tmp"
  bss_add_unrelated_strict_call "$tmp"
  bss_add_own_setup "$tmp"
  report=$(bss_audit "$tmp")
  if ! echo "$report" | grep -q 'first effective command'; then
    echo "FAIL: a strict_setup call outside setup was credited; got: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}

@test "an e2e- file is outside the audit" {
  tmp=$(mktemp -d)
  bss_new_fixture "$tmp"
  bss_rename "$tmp" "e2e-subject.bats"
  report=$(bss_audit "$tmp")
  if [ -n "$report" ]; then
    echo "FAIL: a live-cluster suite was audited as a unit file: $report"
    rm -rf "$tmp"
    false
  fi
  rm -rf "$tmp"
}
