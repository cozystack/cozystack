#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Every hack/*.bats unit file loads hack/test_helper.bash, and this is what
# makes that true of the file someone adds next month.
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
# LIMITS, stated rather than discovered. The scan is lexical and anchored at
# column zero. A `load` indented inside a function body would not run at source
# time and is correctly not credited, but neither is a legitimate one hidden
# behind a conditional -- the audit asks for the one spelling the suite uses,
# and a file with a reason to differ has to change this guard rather than work
# around it. In the other direction the line is credited wherever it appears at
# column zero, including inside a heredoc that writes a fixture .bats, which
# would let a file take credit for text it only generates; no file does that
# today, and the fixtures below assemble the line from printf arguments so this
# guard does not do it to itself. Finally, a green audit says the load is
# present, not that `set -u` was in force for a given assertion -- for that,
# hack/test_helper.bash's own effect is what the mutation check in #3453
# covered: remove the load and a test reading an unset variable stops aborting.
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
# hack/e2e-apps/ holds live-cluster suites the Makefile's wildcard never reaches
# and that packages/core/testing runs against a real cluster; they are outside
# this suite and outside this audit.
bss_units() {
  find "$1" -maxdepth 1 -name '*.bats' ! -name 'e2e-*' | sort
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
    if ! grep -q '^load test_helper$' "$_f"; then
      echo "$_b: does not load the strict-mode helper; add a column-zero \`load test_helper\` line under the file's leading comment block"
      continue
    fi
    # A file-local setup() replaces the helper's without a word from either
    # runner. Match the two spellings bash accepts for a definition at column
    # zero; one indented inside another function is not a definition bats will
    # call, and is not what this looks for.
    if grep -qE '^(function[[:space:]]+)?setup[[:space:]]*\(\)' "$_f" \
      && ! grep -q 'strict_setup' "$_f"; then
      echo "$_b: defines its own setup() and never calls strict_setup, which drops the \`set -u\` the helper exists to restore"
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
