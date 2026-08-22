#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The ban on EXIT-trap cleanup in hack/*.bats, and the debt each file that still
# breaks it declares for itself.
#
# Why the ban. An EXIT trap inside an @test body replaces the one the bats binary
# installs for its own bookkeeping, and a test that then FAILS prints no TAP line
# at all. Not `not ok` -- nothing. The run ends with a warning that it executed
# fewer tests than it planned, so anyone reading the tail of the output, or
# grepping it for `not ok`, sees a green suite. Not hypothetical:
# hack/promote-retag_test.bats reported five passes and no failures while six of
# its eleven tests printed nothing. Clean up at the end of the test body instead.
# Both runners set -e, so on failure the cleanup is unreachable and the scratch
# directory survives for inspection, which is what a failed test wants anyway.
# Fuller reasoning, and the two exceptions -- a self-contained trap inside a
# Chainsaw `script` step, and one inside an explicit subshell -- are in
# docs/agents/e2e-testing.md.
#
# The debt belongs to the file that owes it, and not to a central inventory: put
# the count somewhere else and a change that adds a trap has to edit a suite it
# otherwise never opens, so two changes sharing no line invalidate each other --
# both green against their own base, both merging cleanly, red only once the
# second lands.
#
# The declaration is one comment line in the file's leading comment block, under
# the shebang and above the first line of code:
#
#     # EXIT-TRAP DEBT: 12
#
# There and not merely somewhere, because a .bats file is shell that writes
# shell: the same line inside a heredoc, a fixture or an expected-output string
# is data belonging to one test, and honouring it there would let an unrelated
# fixture excuse a real trap. The leading comment block rather than everything
# above the first @test, because helper functions and their heredocs sit between
# the two; a comment block is the region with no interior.
#
# It is exact, not a ceiling. Adding a trap fails; removing one fails too, and
# the fix is to lower the number, or delete the line once it reaches zero. A file
# with no declaration must hold no traps, which covers the converted files, the
# files that never had one, and every file added tomorrow.
#
# NOT every counted handler is debt. A trap inside an explicit subshell does not
# replace the bats binary's own, so a test that fails inside `( ... )` still
# prints its `not ok` -- verified under Bats 1.14.0 against a test-level trap in
# the same file, where the TAP line vanishes. hack/e2e-test-openapi.bats holds
# one: it kills a background `kubectl proxy` from a trap inside a subshell, and
# moving that to the end of the body would leak the process on failure instead of
# suppressing a report. Its declaration says so, and because the ratchet is
# exact, REMOVING that trap fails too -- the count protects the construct rather
# than scheduling it for deletion.
#
# The limits are worth stating rather than discovering. The scan is lexical: a
# signal computed at runtime (`trap cleanup "$sig"`) is invisible, and so is a
# handler whose quoted action spans physical lines without a backslash, since
# only backslash continuations are folded. In the other direction the word is
# counted where no command exists -- inside a heredoc, or in a string a test
# compares against -- which inflates a file's debt rather than hiding one, and is
# why the fixture writers below assemble the keyword and the signal from separate
# arguments. `trap - EXIT`, which DISARMS a handler, scores as an install for the
# same inflating reason. An exact count catches addition and removal but never
# SUBSTITUTION: swap the openapi file's subshell trap for a test-level one and
# the total stays 1, the guard stays green, and that file's "this one is NOT
# debt" note quietly becomes false.
#
# The scan reads .bats and nothing else, so a handler reaching a test body from a
# sourced .sh is invisible to it. Not theoretical:
# hack/e2e-chainsaw/_lib/run-kubernetes.sh installs two, each benign for its own
# reason rather than by design -- cozy_capture_tenant_talos is declared with `(`
# and so runs in a subshell, and the diagnostics tests DO call it, so flipping
# its two delimiters to braces reinstates that handler in them with the guard
# green; run_kubernetes_test is declared with `{`, the shape that would bite, and
# is benign only because no @test calls it.
#
# The marker is anchored at column zero. That is NOT what protects the example
# above -- the nested `#` does, and relaxing the anchor leaves this file's own
# audit green. What the anchor buys is that a marker indented inside a function
# body or a heredoc somewhere else is not read as that file's declaration, and a
# file that indents its own reports "declares none" and fails loudly rather than
# passing quietly. Reflowing the example above to column zero would make the
# guard demand a debt of twelve from a file holding nothing.
#
# None of that makes the guard a proof that a file installs no handler; it is a
# ratchet over a known and common spelling. A suite reporting zero failures still
# has to have its `1..N` plan reconciled against its `ok` count, which is the
# check that catches whatever the pattern missed.
# -----------------------------------------------------------------------------

# The directory to audit. Under the bats binary that is this file's own
# location; under cozytest.sh, which does not set BATS_TEST_FILENAME, `$0` is
# the runner itself -- and the answer comes out the same only because the runner
# lives in the directory it runs files from. The first test below refuses to
# pass on an empty enumeration, so a wrong answer here fails rather than audits
# nothing.
BATS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"

# Print a file with comment lines dropped and shell line continuations folded, so
# the count below matches a COMMAND rather than a physical line. `trap 'rm -rf
# "$tmp"' \` with `EXIT` on the next line is a working EXIT trap that no
# single-line pattern sees, and in shell that is where the argument usually sits.
bnet_fold() {
  grep -v '^[[:space:]]*#' "$1" \
    | awk '{ line = line $0
             if (line ~ /\\$/) { sub(/\\$/, " ", line); next }
             print line; line = "" }
           END { if (line != "") print line }'
}

# What an installed EXIT handler looks like.
#
# `0` as well as `EXIT`: POSIX names the exit condition signal 0, and both shells
# install the identical handler for it, so a pattern that knows only the spelling
# it has seen tests the spelling and not the construct. `EXIT` anywhere in the
# signal list rather than only as its last word: `trap ... EXIT HUP` installs the
# same exit handler plus one more, and anchoring to end-of-line accepts it. A
# quote around the signal, because `trap cleanup 'EXIT'` installs exactly what
# `trap cleanup EXIT` installs -- the quote is punctuation, not a different
# construct. The quote is optional around the SIGNAL only and never after the
# keyword, so `'trap'` as a shell argument stays unmatched; the fixture writers
# below depend on that to keep their own test data from reading as real traps.
#
# The signal ends at any character that cannot continue an identifier, not at
# whitespace. `trap ... EXIT; cd "$tmp"` and `(trap ... EXIT)` are both real
# handlers with punctuation straight after the word, and a terminator demanding
# whitespace or end-of-line scores them zero -- guard green, no declaration
# asked for. Excluding the identifier characters is what keeps `EXIT_CODE` from
# matching, which is the reason the narrower terminator looked right.
#
# The signal matches in either case, because the shell accepts it in either
# case: `trap ... exit` installs the handler `trap ... EXIT` installs, in bash
# and in dash alike. The folding is spelled into the alternation rather than
# running the whole pattern case-insensitively, since `trap` is a builtin name
# and `TRAP ... EXIT` is not a command anyone installs.
#
# The keyword needs its left boundary as much as the signal needs its right
# one, and for the same reason. `bootstrap ` ends in `trap `, so once the
# terminator accepts punctuation, `talosctl bootstrap -n ... ; do sleep 0.5`
# scores as a handler on the `0`. That is loud rather than silent, but not
# harmless: the documented answer to a red guard is to add a debt line, so a
# file would buy a permanent licence for one real trap in order to quiet a line
# that has none.
#
# Both boundaries are therefore spelled the same way -- any character that
# cannot be part of an identifier -- and NOT as whitespace. Whitespace on the
# left drops `tmp=$(mktemp -d);trap ... EXIT` and `(trap ... EXIT; true)`, which
# are the banned construct behind a semicolon and a paren. The first of those is
# the sharper lesson: the inventory this replaces carried no left boundary at
# all and caught it, so a boundary chosen carelessly here would have narrowed
# coverage while the commit message claimed to widen it.
BNET_TRAP_RE="(^|[^A-Za-z0-9_])trap[[:space:]].*[[:space:]][\"']?([Ee][Xx][Ii][Tt]|0)[\"']?([^A-Za-z0-9_]|\$)"

# How many EXIT traps a file installs.
#
# `|| true` because grep -c exits 1 on a count of zero, which is the answer for
# most of the tree; without it the caller dies under set -e on the first clean
# file and never reaches the comparison it exists for.
bnet_count() {
  bnet_fold "$1" | grep -cE "$BNET_TRAP_RE" || true
}

# How many lines install more than one EXIT handler.
#
# bnet_count counts matching LINES, so `trap a EXIT; trap b EXIT` counts as one
# and a handler appended to a line that already carries one arrives without
# moving the file's total. Deciding where one command ends and the next begins
# needs a shell parser -- a semicolon inside a handler's own quoted action is not
# a separator -- and getting that wrong UNDERCOUNTS, which is the one direction a
# ratchet cannot afford. So refuse the ambiguous line instead of guessing at it.
# One handler per line is what every file in the tree already does.
bnet_ambiguous() {
  bnet_fold "$1" | grep -E "$BNET_TRAP_RE" \
    | awk '{ if (gsub(/(^|[^A-Za-z0-9_])trap[[:space:]]/, "&") > 1) n++ } END { print n + 0 }'
}

# The part of a file the declaration may live in: the leading comment block,
# ending at the first line that is neither a comment nor blank.
#
# Traps are counted over the whole file; the declaration is read from the header
# alone. A .bats file is shell that writes shell -- fixture writers, heredocs of
# expected output, assertion strings -- and the same line appearing in any of
# those is data belonging to one test, not a statement about the file. Reading it
# there would let an unrelated fixture excuse a real trap, and would do it
# silently, because nothing in that test's own diff looks like a declaration.
#
# The leading comment block rather than everything above the first @test: helper
# functions and their heredocs sit between the two, so stopping at the first
# @test would still read a line out of one. A comment block cannot contain a
# heredoc, which makes the narrower rule the one with no interior. It is also
# where a reviewer opening the file lands, which is the reason the declaration
# moved out of the central inventory in the first place.
bnet_header() {
  awk '/^[[:space:]]*#/ || /^[[:space:]]*$/ { print; next } { exit }' "$1"
}

# Report every file in directory $1 whose traps and declaration disagree, one
# line each, and print nothing when they all agree.
#
# Through stdout rather than an exit status: cozytest.sh rewrites a function's
# closing brace into `return 0`, so a status set in here would be discarded
# before the caller could read it, and the check would pass unconditionally.
bnet_audit() {
  find "$1" -name '*.bats' | sort | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    # Path relative to the audited root, not the basename: the scan recurses,
    # and hack/foo.bats and hack/e2e-apps/foo.bats would otherwise report under
    # the same name.
    _b=${_f#"$1"/}
    _amb=$(bnet_ambiguous "$_f")
    if [ "$_amb" -gt 0 ]; then
      echo "$_b: $_amb line(s) name the trap keyword more than once; keep one handler per line, and the word out of trailing comments, so the count is exact"
      continue
    fi
    _n=$(bnet_count "$_f")
    _decls=$(bnet_header "$_f" | grep -c '^# EXIT-TRAP DEBT:' || true)
    if [ "$_decls" -gt 1 ]; then
      echo "$_b: $_decls debt declarations; a file carries one or none"
      continue
    fi
    if [ "$_decls" -eq 0 ]; then
      if [ "$_n" -ne 0 ]; then
        echo "$_b: holds $_n EXIT trap(s) and declares none; clean up at the end of the test body instead, or -- if the line is data rather than a command -- split the keyword from the signal the way the fixtures below do"
      fi
      continue
    fi
    _d=$(bnet_header "$_f" | sed -n 's/^# EXIT-TRAP DEBT:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
    if [ -z "$_d" ]; then
      echo "$_b: the debt declaration names no count"
    elif [ "$_d" -eq 0 ]; then
      echo "$_b: declares a debt of zero; delete the line instead"
    elif [ "$_n" -ne "$_d" ]; then
      echo "$_b: declares $_d EXIT trap(s), holds $_n"
    fi
  done
  return 0
}

# Fixture writers for the tests below. The word and the signal are assembled from
# separate arguments on purpose: this file is scanned by the guard it defines, so
# a fixture spelled out as a literal would be counted as a real trap in it and
# the guard would demand a declaration covering its own test data.
#
# The same constraint reaches the test titles and the failure messages, which are
# code lines rather than comments and so are read as well. `bnet_add_handler`
# rather than `bnet_add_trap`, and "a handler naming EXIT" rather than "a trap
# naming EXIT", because either of the shorter forms puts the word and the signal
# on one line and is matched. Editing this file means reading its own report
# afterwards; the first test below is the one that says so.
bnet_new_fixture() {
  _dir=$1
  printf '%s\n' '#!/usr/bin/env bats' > "$_dir/subject.bats"
  return 0
}

bnet_add_handler() {
  printf "%s 'rm -rf \"\$tmp\"' %s\n" 'trap' "${2:-EXIT}" >> "$1/subject.bats"
  return 0
}

bnet_add_folded_handler() {
  printf "%s 'rm -rf \"\$tmp\"' \\\\\n%s\n" 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

bnet_add_commented_handler() {
  printf "# %s 'rm -rf \"\$tmp\"' %s\n" 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

# Put a declaration where a real file puts it: in the leading comment block, just
# under the shebang, whatever the fixture has appended by now. Rebuilt around
# line 1 rather than inserted with `sed -i`, whose append syntax differs between
# the GNU and BSD versions this suite runs under.
bnet_add_debt() {
  _f=$1/subject.bats
  { head -n 1 "$_f"; printf '%s %s\n' '# EXIT-TRAP DEBT:' "$2"; tail -n +2 "$_f"; } > "$_f.new"
  mv "$_f.new" "$_f"
  return 0
}

# Put a declaration past the header, where it must NOT be honoured.
bnet_append_debt() {
  printf '%s %s\n' '# EXIT-TRAP DEBT:' "$2" >> "$1/subject.bats"
  return 0
}

bnet_add_test_body() {
  printf '%s\n' '@test "something" {' '  :' '}' >> "$1/subject.bats"
  return 0
}

bnet_add_code() {
  printf '%s\n' 'BATS_DIR=.' >> "$1/subject.bats"
  return 0
}

bnet_add_word_containing_trap() {
  printf "  timeout 60 sh -ec 'until talosctl %s -n 192.0.2.11; do sleep 0.5; done'\n" \
    'bootstrap' >> "$1/subject.bats"
  return 0
}

bnet_add_indented_debt() {
  printf '  %s %s\n' '# EXIT-TRAP DEBT:' "$2" >> "$1/subject.bats"
  return 0
}

bnet_add_command_then_handler() {
  printf "a=1;%s 'rm -rf \"\$a\"' %s\n" 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

bnet_add_handler_in_parens() {
  printf "(%s 'kill 1' %s; true)\n" 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

bnet_add_handler_then_command() {
  printf "%s 'rm -rf \"\$a\"' %s; cd /\n" 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

bnet_add_nested_fixture() {
  mkdir -p "$1/nested"
  printf '%s\n' '#!/usr/bin/env bats' > "$1/nested/deep.bats"
  printf "%s 'rm -rf \"\$a\"' %s\n" 'trap' 'EXIT' >> "$1/nested/deep.bats"
  return 0
}

bnet_add_two_handlers_on_one_line() {
  printf "%s 'rm -f \"\$a\"' %s; %s 'rm -f \"\$b\"' %s\n" \
    'trap' 'EXIT' 'trap' 'EXIT' >> "$1/subject.bats"
  return 0
}

@test "every bats file under hack holds exactly the EXIT traps it declares" {
  # The whole point of the guard, run against the tree it governs. Everything
  # below this test exists to prove that this one can fail.
  # Counted with the enumeration bnet_audit actually walks, not a second one:
  # a backstop that asks `ls` whether the `find` found anything answers about
  # the wrong list, and would pass while the audit went vacuously green. That
  # is the failure this file exists to talk about, so it may as well not commit
  # it.
  files=$(find "$BATS_DIR" -name '*.bats' | wc -l)
  [ "$files" -gt 0 ] || { echo "FAIL: found no .bats files to audit at all"; false; }

  offences=$(bnet_audit "$BATS_DIR")
  if [ -n "$offences" ]; then
    echo "FAIL: EXIT-trap cleanup and the declared debt disagree."
    printf '%s\n' "$offences"
    echo "Clean up at the end of the test body, or correct the file's own"
    echo "'# EXIT-TRAP DEBT:' line; see docs/agents/e2e-testing.md."
    false
  fi
}

@test "a trap with no declaration is reported" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: an undeclared trap went unreported: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a declaration matching the file is accepted" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_debt "$tmp" 2

  out=$(bnet_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: an accurate declaration was reported: $out"; false; }
  rm -rf "$tmp"
}

@test "a declaration below the real count is reported" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_debt "$tmp" 1

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: declares 1 EXIT trap(s), holds 2"*) ;;
    *) echo "FAIL: a grown trap count went unreported: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a declaration above the real count is reported" {
  # The ratchet is exact rather than a ceiling. A file that sheds a trap has to
  # say so, which is what keeps the number from rotting upward and quietly
  # licensing traps somebody removed years earlier.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_debt "$tmp" 3

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: declares 3 EXIT trap(s), holds 1"*) ;;
    *) echo "FAIL: a stale declaration went unreported: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a trap folded across a line continuation is counted" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_folded_handler "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a trap whose signal sits on the next line went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a trap installed on signal zero is counted" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp" 0

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: the POSIX spelling of the exit condition went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a handler naming EXIT ahead of another signal is counted" {
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp" 'EXIT HUP'

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a handler listing EXIT before another signal went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a trap inside a comment is not counted" {
  # Every converted file explains the ban in prose next to the cleanup it
  # replaced. A check that read the explanation as a violation would push people
  # to delete the explanation, which is the part that stops the next person
  # reinstating the trap.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_commented_handler "$tmp"

  out=$(bnet_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: a trap named in a comment was counted: $out"; false; }
  rm -rf "$tmp"
}

@test "a declared debt of zero is reported" {
  # A file that owes nothing says so by carrying no line, not by carrying a line
  # that says nothing. Otherwise a fully converted file keeps a declaration that
  # reads as an allowance the next reader has no reason to question.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_debt "$tmp" 0

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: declares a debt of zero"*) ;;
    *) echo "FAIL: an empty declaration was accepted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "an indented declaration is not honoured" {
  # The marker is anchored at column zero. Not, as an earlier version of this
  # comment claimed, to protect the header's own `# EXIT-TRAP DEBT: 12` example
  # -- that one is nested behind a second `#` and stays unmatched either way;
  # relaxing the anchor leaves this file's audit green, which is how the claim
  # was caught. What the anchor buys is that a marker indented inside a function
  # body or a heredoc somewhere else is not read as that file's declaration.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_indented_debt "$tmp" 1
  bnet_add_handler "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: an indented declaration was honoured: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a handler preceded by punctuation is counted" {
  # The keyword's left boundary is any character that cannot be part of an
  # identifier, not whitespace specifically. `tmp=$(mktemp -d);trap ... EXIT`
  # and `(trap ... EXIT; true)` both sit straight behind punctuation, and both
  # are the banned construct. Requiring a space would be a REGRESSION -- the
  # inventory this replaces had no left boundary at all and caught the first of
  # them -- so the two boundaries have to be spelled the same way round.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_command_then_handler "$tmp"
  bnet_add_handler_in_parens "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 2 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a handler behind punctuation went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a word merely ending in trap is not counted" {
  # `bootstrap ` contains `trap `, and once the terminator accepts punctuation
  # the `0` in `sleep 0.5` closes the match: a talosctl bootstrap line scores as
  # a handler. Loud, but not harmless -- the documented answer to a red guard is
  # to add a debt line, so a file would take a permanent licence for one real
  # trap to silence a line that has none. The keyword needs its left boundary
  # for the same reason the signal needed its right one.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_word_containing_trap "$tmp"

  out=$(bnet_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: a word ending in trap was counted: $out"; false; }
  rm -rf "$tmp"
}

@test "a handler naming the signal in lower case is counted" {
  # Signal names are case-insensitive to the shell: `trap ... exit` installs the
  # same handler as `trap ... EXIT`, verified in both bash and dash. Only the
  # SIGNAL is; `trap` itself is a builtin name and stays lower case, which is
  # why the pattern folds case on the signal alternation rather than being run
  # case-insensitively end to end.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp" exit

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a lower-case signal name went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a handler followed by another command on the same line is counted" {
  # The signal does not have to end the line. `trap ... EXIT; cd "$tmp"` and a
  # handler wrapped in `( ... )` both put punctuation straight after the word,
  # and a terminator that insists on whitespace or end-of-line silently scores
  # them zero -- a real handler, no declaration needed, guard green. Same class
  # as the quoted signal, one keystroke away.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler_then_command "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a handler followed by a semicolon went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a bats file in a subdirectory is audited too" {
  # The ban is on every bats file under hack/, and hack/e2e-apps/ already holds
  # two. A flat glob would leave the claim about "every file added later" false
  # for anything a directory down.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_nested_fixture "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"nested/deep.bats: holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a nested bats file went unaudited or lost its path: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a declaration naming no count is reported" {
  # The parser takes digits. A declaration whose count is a word, or missing,
  # parses to nothing, and an unguarded comparison would then treat the file as
  # if it had said something.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_debt "$tmp" "twelve"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: the debt declaration names no count"*) ;;
    *) echo "FAIL: a countless declaration was accepted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a handler whose signal is quoted is counted" {
  # `trap cleanup 'EXIT'` installs the handler `trap cleanup EXIT` installs. A
  # pattern that demands a bare word tests the punctuation rather than the
  # construct, and the quoted form is one keystroke away from whoever wants the
  # guard to look past them.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp" "'EXIT'"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a quoted signal went uncounted: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "two handlers sharing one line are reported rather than counted as one" {
  # The count is a count of LINES, so a second handler appended to a line that
  # already carries one arrives free: the file's total does not move and the
  # declaration keeps matching. Rather than guess at command boundaries -- which
  # needs a shell parser, and whose failure mode is an UNDERCOUNT, the direction
  # this guard cannot afford -- refuse the ambiguous line and say so. One handler
  # per line is what the tree already does.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_two_handlers_on_one_line "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: 1 line(s) name the trap keyword more than once"*) ;;
    *) echo "FAIL: a doubled-up line was silently counted once: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a declaration below the leading comment block is not honoured" {
  # The header ends at the first line of code, not at the first @test. A .bats
  # file runs shell before its tests -- helper functions, fixture writers, a
  # heredoc of expected output -- and a line inside any of that is data, not a
  # statement about the file. Ending the header at the first @test would still
  # read it.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_code "$tmp"
  bnet_append_debt "$tmp" 1
  bnet_add_handler "$tmp"

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a declaration past the first code line excused a trap: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a declaration below the first test is not honoured" {
  # Only the header counts, so that a line the file merely CONTAINS -- inside a
  # heredoc, in a fixture a test writes, in an expected-output string -- cannot
  # excuse a real trap. Such a line is written to be read as data by the test
  # around it, and its author has no idea it is also being read as a promise
  # about the file. Narrowing to the header also puts the declaration where a
  # reviewer opening the file sees it, which is the whole reason it moved out of
  # the central inventory.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_test_body "$tmp"
  bnet_append_debt "$tmp" 1

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: holds 1 EXIT trap(s) and declares none"*) ;;
    *) echo "FAIL: a declaration past the first test excused a trap: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a second declaration in one file is reported" {
  # Two declarations make the file's own statement ambiguous, and a parser that
  # silently takes the first would let the losing one drift without ever saying
  # which number is in force.
  tmp=$(mktemp -d)
  bnet_new_fixture "$tmp"
  bnet_add_handler "$tmp"
  bnet_add_debt "$tmp" 1
  bnet_add_debt "$tmp" 9

  out=$(bnet_audit "$tmp")
  case "$out" in
    *"subject.bats: 2 debt declarations"*) ;;
    *) echo "FAIL: contradictory declarations were accepted: $out"; false ;;
  esac
  rm -rf "$tmp"
}
