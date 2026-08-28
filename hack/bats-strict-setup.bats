#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Every hack/*.bats unit file loads hack/test_helper.bash exactly once, and this
# is what makes that true of the file someone adds next month.
#
# WHAT THE HELPER IS FOR. Bats enforces `set -e` on a test body but not `set -u`,
# while hack/cozytest.sh -- the runner this suite used before #3453 -- ran every
# body under `set -eu -x`. Without the helper a test that reads an unset variable
# sees an empty string and passes, which is a test asserting nothing while
# reporting `ok`.
#
# WHY IT IS A PER-FILE LINE. Because bats has no other hook. Its default
# `setup()` is defined in bats-core's own test_functions.bash and sourced BEFORE
# the test file, so nothing outside the file can replace it. `setup_suite` --
# whether from --setup-suite-file or an auto-discovered setup_suite.bash -- runs
# once in bats-exec-suite's process and never in the process that runs a test.
# And the two environment routes that do cross a process boundary, SHELLOPTS and
# BASH_ENV, are exported by definition, so they reach the hack/*.sh scripts the
# tests exercise as subprocesses and change the code under test rather than the
# tests. `load` is the only mechanism there is.
#
# WHY IT IS AUDITED RATHER THAN REMEMBERED. The helper landed covering 32 of 32
# unit files and was found later covering 32 of 63 -- not because anyone removed
# a line, but because 27 files were added after it and nothing anywhere held the
# list of files that were supposed to have one. A requirement whose only record
# is the diff that introduced it decays at exactly the rate the tree grows, and
# it decays silently: an uncovered file still runs, still passes, and its tests
# are simply weaker than they read.
#
# So the record is this file, and its input is the tree rather than a list. It
# enumerates hack/*.bats minus hack/e2e-*.bats -- the same set the Makefile
# hands the runner, and one of the tests below asserts that the two agree by
# asking make for its own answer rather than trusting this file's copy of the
# rule. A new unit file is in the audit's input from the moment it exists, so
# the gap closes at the commit that opens it. There is nothing to update, which
# is the property the first version lacked: it required an author to remember,
# and the mechanism that requires memory is the mechanism that drifts.
#
# THE SECOND WAY THE STRICTNESS GOES MISSING is a file that loads the helper and
# then defines its own `setup()`, which silently replaces the helper's. That is
# checked too: a file-local `setup()` must call `strict_setup`. No file in the
# tree defines one today, which is precisely when a guard is cheap to add.
#
# LIMITS, stated rather than discovered. The load scan is lexical and anchored
# at column zero. A `load` indented inside a function body would not run at source
# time and is correctly not credited, but neither is a legitimate one hidden
# behind a conditional -- the audit asks for the one spelling the suite uses,
# and a file with a reason to differ has to change this guard rather than work
# around it. The setup scan is deliberately fail-closed: it recognizes the
# common Bash declaration spellings wherever they are indented, but accepts one
# column-zero `setup() {` only, and credits only an executable `strict_setup`
# line inside that body. This rejects ambiguous nesting and keeps comments or an
# unrelated helper from satisfying the guard. In the other direction the load
# line is credited wherever it appears at column zero, including inside a heredoc
# that writes a fixture .bats, which would let a file take credit for text it only
# generates; no file does that today, and the fixtures below assemble the line
# from printf arguments so this guard does not do it to itself. Finally, a green
# audit says the load is present exactly once, not that `set -u` was in force for
# a given assertion -- for that, hack/test_helper.bash's own effect is what the
# mutation check in #3453 covered: remove the load and a test reading an unset
# variable stops aborting.
# -----------------------------------------------------------------------------

load test_helper

# The directory to audit. Under the bats binary that is this file's own
# location; under cozytest.sh, which does not set BATS_TEST_FILENAME, `$0` is
# the runner itself -- and the answer comes out the same only because the runner
# lives in the directory it runs files from. The first test below refuses to
# pass on an empty enumeration, so a wrong answer here fails rather than audits
# nothing.
BSS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
BSS_REPO_ROOT="$(cd "$BSS_DIR/.." && pwd)"

# The unit files: hack/*.bats that are not hack/e2e-*.bats.
#
# Non-recursive, and the exclusion is on the basename, because that is what
# `$(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))` in the Makefile means.
# A shell glob keeps this portable to the macOS hook instead of relying on
# GNU find's non-POSIX `-maxdepth` flag.
# hack/e2e-apps/ holds live-cluster suites the Makefile's wildcard never reaches
# and that packages/core/testing runs against a real cluster; they are outside
# this suite and outside this audit.
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
    if [ "$_canonical_count" -eq 1 ] \
      && ! sed -n '/^setup() {$/,/^}$/p' "$_f" \
        | grep -qE '^[[:space:]]*strict_setup[[:space:]]*$'; then
      echo "$_b: defines its own setup() and never calls strict_setup as a command, which drops the \`set -u\` the helper exists to restore"
    fi
  done
  return 0
}

# Fixture writers. The load line is assembled from a printf argument rather than
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
  printf '%s\n' 'setup() {' '  strict_setup' '  export FIXTURE=1' '}' >> "$1/subject.bats"
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
  # Compared through files rather than process substitution: this file also runs
  # under hack/cozytest.sh, whose #!/bin/sh is dash on the CI runner.
  if ! diff -u "$tmp/theirs" "$tmp/mine" > "$tmp/delta"; then
    echo "FAIL: the audited set and \$(BATS_UNIT_FILES) disagree (-Makefile +audit):"
    cat "$tmp/delta"
    rm -rf "$tmp"
    false
  fi
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
  if ! echo "$report" | grep -q 'never calls strict_setup'; then
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
  if ! echo "$report" | grep -q 'never calls strict_setup as a command'; then
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
  if ! echo "$report" | grep -q 'never calls strict_setup as a command'; then
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
