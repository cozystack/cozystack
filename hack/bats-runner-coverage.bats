#!/usr/bin/env bats

# Every .bats file under hack/ must be run by something.
#
# Two files once sat in hack/e2e-apps/ that no runner invoked: #2826 deleted the
# `test-apps-%:` pattern rule that used to run `hack/e2e-apps/$*.bats`, and #3176
# then added two new suites into the emptied directory. Git raises no conflict
# when one branch deletes a directory's contents and another adds files to it, so
# the only signal was a directory name that still looked live. Three commits of
# review went into tests that never executed once.
#
# There are exactly two runners, and that is a closed list by design:
#
#   A. Makefile's `bats-unit-tests` walks $(wildcard hack/*.bats) minus
#      hack/e2e-%.bats. The glob is ONE level deep, which is the whole bug: it
#      says nothing at all about hack/<subdir>/.
#   B. packages/core/testing/Makefile names its suites literally, one
#      `hack/cozytest.sh <path>` per recipe line, and runs them inside the e2e
#      sandbox.
#
# A file matching neither is dead code. A third runner appearing shows up here as
# a false offender rather than as silence, which is correct: the runner inventory
# is the thing this file is about, so a new runner registers here or it is not a
# runner as far as this guard is concerned.
#
# No allowlist and no debt-declaration mechanism, deliberately. The project's
# off-switch is the `.disabled` suffix (hack/select-e2e.sh:254-255 states the
# convention for Chainsaw suites), and `find -name '*.bats'` stops seeing a
# parked file on its own. An in-test list of exempt paths would be the same
# artefact class as the runner that got deleted while its documentation stayed.
#
# Why the Makefiles are parsed rather than asked: hack/common-envs.mk is included
# at Makefile:3, and when COZYSTACK_VERSION comes back empty it runs `git remote
# add upstream` and `git fetch upstream --tags` in `$(shell ...)` AT PARSE TIME.
# `make -p`, `make -n` and `$(info ...)` all pay that, so asking make would make a
# unit test mutate git remotes and reach the network to answer a question about
# filenames. Make also cannot answer for runner B at all -- those three suites are
# literal recipe text, so `make -np` hands back the same text to grep, one
# fragile evaluation later. The cost of grepping is that the model can drift out
# of step with the Makefile it models; the second test below pins the modelled
# lines verbatim so that drift fails loudly instead of quietly widening the set
# of files this guard believes are covered.
#
# Scope, so the pin is not read as covering more than it does. No test in the
# unit suite can catch deletion of `bats-unit-tests` itself, or of the `make
# unit-tests` step in pull-requests.yaml -- this guard would simply stop running
# with everything else. The `unit-tests:`-names-`bats-unit-tests` pin below covers
# the dependency being dropped while the file still runs; past that the hole is
# structural. Chainsaw suites are out of scope: `chainsaw test` does its own
# discovery, and hack/select-e2e_test.bats already pins the suite-to-source
# mapping in both directions. Reachability here is at file level; it is not a
# proof that CI invokes every target.
#
# Note also what an `e2e-` prefix does and does not mean. It arms the cluster
# captures in hack/cozytest.sh (pinned by hack/cozytest-capture-gate.bats) and it
# excludes a top-level file from runner A. It has never been a claim that a runner
# exists. Conflating the two is how the original bug stayed invisible.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
E2E_MAKEFILE_REL="packages/core/testing/Makefile"

# A literal tab, for matching make recipe lines. Only tab-led lines are recipes;
# a `#` inside one is shell, not a make comment, so comments are stripped first
# and the recipe filter applied second.
TAB="$(printf '\t')"

# Drop make comment lines, so a commented-out runner can never satisfy the
# contract. grep exits 1 when it selects nothing, which is legitimate here, and 2
# on a real error; the rc check turns only the latter into a failure. Under
# hack/cozytest.sh the status is discarded anyway -- its translator appends
# `return 0` to every line that is exactly `}`, file-level helpers included -- so
# every test below asserts its extraction is non-empty before comparing, and a
# swallowed error surfaces as empty input rather than as a pass.
brc_code_lines() {
  local rc=0
  grep -v '^[[:space:]]*#' || rc=$?
  [ "$rc" -le 1 ]
}

# Every .bats file under $1/hack, printed relative to $1.
#
# Relative rather than basename: the scan recurses, and hack/foo.bats and
# hack/e2e-apps/foo.bats must not report under one name. Same reason as
# bnet_audit in hack/bats-no-exit-trap.bats.
brc_all_bats() {
  find "$1/hack" -name '*.bats' | sort | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    echo "${_f#"$1"/}"
  done
}

# Runner A's selection: $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats)).
brc_unit_reachable() {
  find "$1/hack" -maxdepth 1 -name '*.bats' ! -name 'e2e-*' | sort | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    echo "${_f#"$1"/}"
  done
}

# Runner B's selection: paths named on a recipe line, each immediately after
# `hack/cozytest.sh`.
#
# Requiring the token to FOLLOW cozytest.sh is what stops a stale mention in a
# variable, a comment already dropped above, or a dead assignment from counting as
# a runner. The trailing quote of the surrounding `sh -c '...'` falls outside the
# match because the pattern ends at `.bats`; a quote leading the token, were a
# path ever written quoted, is removed after extraction, since no path here
# contains one.
brc_named_suites() {
  brc_code_lines < "$1/$E2E_MAKEFILE_REL" \
    | grep "^$TAB" \
    | grep -oE "hack/cozytest\.sh[[:space:]]+[^[:space:]]+\.bats" \
    | sed "s|.*cozytest\.sh[[:space:]]*||" \
    | tr -d "\"'" \
    | sort -u
}

# Named suites whose path is not a literal -- the shape of the deleted
# `hack/e2e-apps/$*.bats` pattern rule.
#
# Reported rather than expanded. A pattern rule nobody invokes is exactly as dead
# as no rule at all, so treating one as coverage would reintroduce this bug in a
# new shape. If a pattern runner is ever wanted back, teach this guard the pattern
# form on purpose.
brc_unresolvable_suites() {
  brc_named_suites "$1" | grep '\$' || true
}

# Report every reachability offence under root $1, one line each, and print
# nothing when there are none.
#
# Through stdout rather than an exit status: hack/cozytest.sh rewrites a
# function's closing brace into `return 0`, so a status set in here would be
# discarded before the caller could read it and the check would pass
# unconditionally. Nothing is accumulated in a variable across a pipeline either,
# because `find | while read` runs in a subshell under dash.
brc_audit() {
  _root=$1
  _reach=$( { brc_unit_reachable "$_root"; brc_named_suites "$_root" | grep -v '\$' || true; } | sort -u )

  brc_all_bats "$_root" | while IFS= read -r _b; do
    if printf '%s\n' "$_reach" | grep -Fxq "$_b"; then
      continue
    fi
    echo "$_b: run by nothing. Runner A walks hack/*.bats one level deep and skips hack/e2e-*; $E2E_MAKEFILE_REL names no suite at this path. Add a runner there, port it to hack/e2e-chainsaw/, or park it as *.bats.disabled."
  done

  # The inverse: a suite the e2e Makefile still names after the file moved or was
  # renamed. Cheap here, and expensive anywhere else -- a stale name in
  # test-openapi surfaces ~40 minutes into the e2e job, and in prepare-cluster or
  # install-cozystack it wedges the whole bootstrap before a single test runs.
  brc_named_suites "$_root" | grep -v '\$' | while IFS= read -r _n; do
    if [ ! -f "$_root/$_n" ]; then
      echo "$E2E_MAKEFILE_REL runs $_n, which does not exist on disk."
    fi
  done

  brc_unresolvable_suites "$_root" | while IFS= read -r _u; do
    echo "$E2E_MAKEFILE_REL names $_u through a make variable; this guard resolves literal paths only. Name the suites literally, or teach the guard the pattern form on purpose."
  done
}

@test "every bats file under hack is run by a runner" {
  # The point of the file, run against the tree it governs. Everything below
  # exists to prove this test can fail.
  [ -f "$REPO_ROOT/Makefile" ] || { echo "FAIL: no root Makefile at $REPO_ROOT"; false; }
  [ -f "$REPO_ROOT/$E2E_MAKEFILE_REL" ] || { echo "FAIL: no $E2E_MAKEFILE_REL"; false; }

  # Counted with the enumeration brc_audit actually walks, not a second one: a
  # backstop that asks `ls` whether the `find` found anything answers about the
  # wrong list and would pass while the audit went vacuously green.
  files=$(brc_all_bats "$REPO_ROOT" | wc -l)
  [ "$files" -gt 0 ] || { echo "FAIL: found no .bats files to audit at all"; false; }

  # And the other premise: runner B naming nothing is precisely the #2826 shape,
  # so an empty extraction must fail rather than mark every e2e suite covered by
  # nothing or -- worse, once runner A is the only source -- reported as fine.
  named=$(brc_named_suites "$REPO_ROOT" | wc -l)
  [ "$named" -gt 0 ] || { echo "FAIL: $E2E_MAKEFILE_REL runs no bats suite at all -- either the e2e bootstrap was deleted or this extraction broke"; false; }

  offences=$(brc_audit "$REPO_ROOT")
  if [ -n "$offences" ]; then
    echo "FAIL: a bats file under hack/ is reachable by no runner."
    printf '%s\n' "$offences"
    echo "Runners: Makefile's bats-unit-tests (hack/*.bats, one level, non-e2e)"
    echo "and $E2E_MAKEFILE_REL (suites named literally)."
    echo "See docs/agents/e2e-testing.md."
    false
  fi
}

@test "the modelled runner selection still matches the Makefile" {
  # The one direction that would otherwise be silent. If someone narrows the
  # glob, brc_unit_reachable keeps calling files reachable that no longer are,
  # and the audit above stays green while coverage shrinks. Pinning the modelled
  # lines verbatim turns that into a failure naming the model rather than forty
  # phantom orphans.
  makefile=$(brc_code_lines < "$REPO_ROOT/Makefile")
  [ -n "$makefile" ] || { echo "FAIL: root Makefile extraction is empty"; false; }

  printf '%s\n' "$makefile" | grep -qF 'BATS_UNIT_FILES := $(filter-out hack/e2e-%.bats,$(wildcard hack/*.bats))' \
    || { echo "FAIL: bats-unit-tests no longer selects files the way brc_unit_reachable models it (one level, hack/e2e-* excluded). Update the model in hack/bats-runner-coverage.bats in the same change."; false; }

  printf '%s\n' "$makefile" | grep -qF 'hack/cozytest.sh "$$f"' \
    || { echo "FAIL: bats-unit-tests no longer runs its files through hack/cozytest.sh; the reachability model assumes it does."; false; }

  printf '%s\n' "$makefile" | grep -qE '^unit-tests:.*bats-unit-tests' \
    || { echo "FAIL: unit-tests no longer depends on bats-unit-tests, so nothing in CI runs the bats sweep -- this guard included."; false; }
}

# A miniature root holding just what brc_audit reads: a hack/ tree and an e2e
# Makefile. Fixtures are written with printf and never a heredoc, because
# hack/cozytest.sh harvests any line starting with `@test "` into this suite --
# a heredoc of test source would become phantom tests here.
brc_fixture() {
  _d=$(mktemp -d)
  mkdir -p "$_d/hack" "$_d/packages/core/testing"
  : > "$_d/$E2E_MAKEFILE_REL"
  echo "$_d"
}

brc_add_suite_file() {
  mkdir -p "$(dirname "$1/$2")"
  printf '#!/usr/bin/env bats\n' > "$1/$2"
}

brc_add_runner_line() {
  printf '%s:\n%sdocker exec "${SANDBOX_NAME}" sh -c '\''cd /workspace && hack/cozytest.sh %s'\''\n' \
    "$(basename "$2" .bats)" "$TAB" "$2" >> "$1/$E2E_MAKEFILE_REL"
}

@test "a nested suite with no runner is reported" {
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-apps/orphan.bats
  brc_add_runner_line "$d" hack/e2e-install-cozystack.bats
  brc_add_suite_file "$d" hack/e2e-install-cozystack.bats

  out=$(brc_audit "$d")
  printf '%s\n' "$out" | grep -q 'hack/e2e-apps/orphan.bats: run by nothing' \
    || { echo "FAIL: the audit did not report an unreachable nested suite. Got: $out"; rm -rf "$d"; false; }
  rm -rf "$d"
}

@test "a nested suite that IS named by a runner is not reported" {
  # The positive control. Without it every other assertion here is satisfied by
  # an audit that reports every file it sees.
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-apps/wired.bats
  brc_add_runner_line "$d" hack/e2e-apps/wired.bats

  out=$(brc_audit "$d")
  if printf '%s\n' "$out" | grep -q 'hack/e2e-apps/wired.bats'; then
    echo "FAIL: a nested suite with a runner line was reported anyway. Got: $out"
    rm -rf "$d"
    exit 1
  fi
  rm -rf "$d"
}

@test "a top-level e2e- suite with no runner is reported" {
  # Runner A filters hack/e2e-* out, so a top-level e2e suite is only reachable
  # by being named. This class has no coverage at all today.
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-newthing.bats
  brc_add_runner_line "$d" hack/e2e-install-cozystack.bats
  brc_add_suite_file "$d" hack/e2e-install-cozystack.bats

  out=$(brc_audit "$d")
  printf '%s\n' "$out" | grep -q 'hack/e2e-newthing.bats: run by nothing' \
    || { echo "FAIL: an unnamed top-level e2e- suite was not reported. Got: $out"; rm -rf "$d"; false; }
  rm -rf "$d"
}

@test "a plain top-level suite is reachable through the unit glob" {
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/plain.bats
  brc_add_runner_line "$d" hack/e2e-install-cozystack.bats
  brc_add_suite_file "$d" hack/e2e-install-cozystack.bats

  out=$(brc_audit "$d")
  if printf '%s\n' "$out" | grep -q 'hack/plain.bats'; then
    echo "FAIL: a plain top-level suite was reported as unreachable. Got: $out"
    rm -rf "$d"
    exit 1
  fi
  rm -rf "$d"
}

@test "a parked .bats.disabled suite is invisible rather than an offence" {
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-apps/parked.bats
  mv "$d/hack/e2e-apps/parked.bats" "$d/hack/e2e-apps/parked.bats.disabled"
  brc_add_runner_line "$d" hack/e2e-install-cozystack.bats
  brc_add_suite_file "$d" hack/e2e-install-cozystack.bats

  out=$(brc_audit "$d")
  if printf '%s\n' "$out" | grep -q 'parked'; then
    echo "FAIL: a parked suite was reported. The .disabled suffix is the off-switch. Got: $out"
    rm -rf "$d"
    exit 1
  fi
  rm -rf "$d"
}

@test "a commented-out runner line does not satisfy the contract" {
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-apps/orphan.bats
  printf '#%sdocker exec x sh -c "cd /workspace && hack/cozytest.sh hack/e2e-apps/orphan.bats"\n' "$TAB" \
    >> "$d/$E2E_MAKEFILE_REL"
  brc_add_runner_line "$d" hack/e2e-install-cozystack.bats
  brc_add_suite_file "$d" hack/e2e-install-cozystack.bats

  out=$(brc_audit "$d")
  printf '%s\n' "$out" | grep -q 'hack/e2e-apps/orphan.bats: run by nothing' \
    || { echo "FAIL: a commented-out runner line was accepted as coverage. Got: $out"; rm -rf "$d"; false; }
  rm -rf "$d"
}

@test "a named suite with no file on disk is reported" {
  d=$(brc_fixture)
  brc_add_runner_line "$d" hack/e2e-renamed-away.bats

  out=$(brc_audit "$d")
  printf '%s\n' "$out" | grep -q 'runs hack/e2e-renamed-away.bats, which does not exist on disk' \
    || { echo "FAIL: a stale runner name was not reported. Got: $out"; rm -rf "$d"; false; }
  rm -rf "$d"
}

@test "a suite named through a make variable is reported as unresolvable" {
  d=$(brc_fixture)
  brc_add_suite_file "$d" hack/e2e-apps/orphan.bats
  printf 'test-apps-%%:\n%sdocker exec x sh -c "cd /workspace && hack/cozytest.sh hack/e2e-apps/$*.bats"\n' "$TAB" \
    >> "$d/$E2E_MAKEFILE_REL"

  out=$(brc_audit "$d")
  printf '%s\n' "$out" | grep -q 'through a make variable' \
    || { echo "FAIL: a pattern-rule runner was not reported as unresolvable. Got: $out"; rm -rf "$d"; false; }
  # And it must not be mistaken for coverage of the file it would have matched.
  printf '%s\n' "$out" | grep -q 'hack/e2e-apps/orphan.bats: run by nothing' \
    || { echo "FAIL: a pattern-rule runner was treated as covering its would-be match. Got: $out"; rm -rf "$d"; false; }
  rm -rf "$d"
}
