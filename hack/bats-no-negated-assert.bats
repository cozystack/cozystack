#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# The ban on `!`-negated assertions in .bats files, and why the form that reads
# most naturally is the one form that cannot fail.
#
# POSIX exempts a command whose return value is inverted with `!` from errexit,
# and neither runner this repository uses recovers what that throws away.
#
# hack/cozytest.sh turns each @test into a shell function with `set -e` at the
# top and `return 0` at the bottom, so a failing `! cmd` neither aborts the
# function nor survives as its return value: it prints a pass whatever the
# assertion found. The bats binary looks stricter and mostly is not. Its ERR
# trap carries the same exemption -- `set -eE; trap ... ERR; ! true` runs the
# trap not at all and exits zero -- so the only negated assertion it reports is
# one standing LAST in the body, where the inverted status becomes the test
# function's own. Move an `echo` below it and bats passes the test too.
#
# So this is not a disagreement between runners to be settled by picking the
# stricter one. Measured on a fixture where each assertion must fail: bare
# `! true`, a negated pipeline, a negated `[ ... ]`, one inside a `for` body,
# and one following `;`, `&&` or `||` all pass under both, and the tail-position
# case is the single shape the two answer differently.
#
# The damage is concentrated rather than spread, because `!` is the natural way
# to spell "this must NOT appear", and an absence is precisely what no other
# assertion in the test covers. A suite can hold a full set of them and be, on
# that half of its contract, a suite that passes on any input.
#
# Write the absence so that it fails:
#
#     if grep -q FORBIDDEN "$log"; then echo "FAIL: <what must not be there>"; false; fi
#
# The `false` is what the runner sees. This is already the form used wherever
# the trap was noticed one file at a time; the rule only makes it universal.
#
# No exception for `! cmd || { echo ...; exit 1; }`, which does work -- the `||`
# reads the inverted status and the `exit` leaves the test. Admitting it means
# the scan has to parse the tail of every statement to tell a guarded negation
# from a bare one, over a set of working spellings that is open-ended, and a
# scan that guesses wrong there is green on a real one. One rule, no tail.
#
# `if !`, `elif !`, `while !` and `until !` are untouched: errexit is not what
# decides a condition, and inverting one is ordinary shell. `[ ! -f x ]` and
# `!=` are not negated commands at all.
#
# The scan is lexical, with the limits that implies. A statement assembled at
# runtime is invisible to it. In the other direction the form is counted where
# no command exists -- inside a heredoc, or in a string a test compares against
# -- which reports a file that holds nothing rather than passing one that does,
# and is why the fixture writers below build the token from a separate argument.
#
# It knows the statement boundaries written into it and no others, so the test
# below names the shapes it was measured against rather than claiming the
# language. Those are the POSIX separators -- `;` `&` `&&` `||` `{` `(` the `)`
# closing a case pattern, and the words `then` `do` `else`. Four known
# over-counts come with that and are left alone, all erring loud: `( ! cmd )`
# does propagate its status and is reported anyway; a `!` after a `&` used as a
# redirection rather than a separator would be too, if one ever began a
# statement; `x=$(true || ! false)` is reported although the `||` reads the
# inverted status and the assignment takes it; and a condition continued by a
# trailing `&&` rather than a backslash -- `if foo &&` on one line, `! bar; then`
# on the next -- is reported because the strip only recognises a condition it
# can see the end of. Each costs a rewrite; the opposite error costs a silent
# pass, which is why the list grows by naming rather than by narrowing.
#
# The verdict travels on stdout and not in an exit status. cozytest.sh appends
# `return 0` before every closing brace at column zero, plain helper functions
# included, so a helper whose answer is the status of its LAST command returns
# success under this runner and its real status under bats. An explicit `return`
# from inside the body still carries -- it runs first -- which is what makes the
# masked case easy to write by accident and hard to see. A guard whose result
# fell out of its final command would be green unconditionally: the same defect
# it exists to ban, one level up.
# -----------------------------------------------------------------------------

# The directory to audit. Under the bats binary this file's own location; under
# cozytest.sh, which sets no BATS_TEST_FILENAME, `$0` is the runner, and the
# answer agrees only because the runner lives beside the files it runs. The
# first test refuses to pass on an enumeration that missed this file, so a wrong
# answer here fails instead of auditing nothing.
BATS_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
RUNNER="$BATS_DIR/cozytest.sh"

# Report each statement in file $1 whose first token is `!`, one line each,
# naming the physical line the statement STARTS on. Whole-line comments are
# blanked rather than dropped so the numbering survives them, and backslash
# continuations are folded so a statement split across lines is read as one.
cnb_scan() {
  awk -v name="${2:-$1}" '
    {
      raw = $0
      if (raw ~ /^[[:space:]]*#/) raw = ""
      if (buf == "") start = FNR
      buf = buf raw
      if (buf ~ /\\$/) { sub(/\\$/, " ", buf); next }
      line = buf; buf = ""

      if (line ~ /^[[:space:]]*(if|elif|while|until)[[:space:]]/) {
        if (match(line, /;[[:space:]]*(then|do)([[:space:]]|$)/))
          line = substr(line, RSTART + RLENGTH)
        else
          next
      }

      # A keyword may be preceded only by whitespace or by `;` -- every other
      # separator in front of `then`, `do` or `else` is a shell syntax error,
      # checked with `sh -n` rather than recalled -- so those two characters are
      # the complete set and the class is closed rather than patched. The leading
      # space covers a keyword in column zero, which has nothing in front of it
      # at all. Miss either and the negation after the keyword never opens a
      # segment, which is silent, and this scan errs loud or not at all. No
      # apostrophes in here: the awk program is single-quoted.
      line = " " line
      n = split(line, seg, /(&&|&|\|\||;|\{|\(|\)|[[:space:];](then|do|else)[[:space:]])/)
      for (i = 1; i <= n; i++)
        if (seg[i] ~ /^[[:space:]]*![[:space:]]/) {
          s = seg[i]; sub(/^[[:space:]]*/, "", s)
          print name ":" start ": " s
          break
        }
    }
  ' "$1"
}

# Every offending statement under directory $1, and nothing when there are none.
cnb_audit() {
  find "$1" -name '*.bats' | sort | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    cnb_scan "$_f" "${_f#"$1"/}"
  done
  return 0
}

# Fixture writers. The token arrives as a separate argument on purpose: this
# file is read by the scan it defines, and a fixture spelled out literally would
# be reported as an offence in the guard's own source.
cnb_new_fixture() {
  printf '%s\n' '#!/usr/bin/env bats' > "$1/subject.bats"
  return 0
}

cnb_append() {
  _dir=$1; shift
  printf "$@" >> "$_dir/subject.bats"
  return 0
}

@test "the audit resolves its root to this file's own directory" {
  files="$(find "$BATS_DIR" -name '*.bats' | wc -l)"
  [ "$files" -gt 1 ]
  # Reaching SOME file is not the claim -- reaching this one is, because a root
  # resolved from the runner instead of the suite would still enumerate a
  # plausible directory and report an audit of somewhere else as clean.
  find "$BATS_DIR" -name '*.bats' | grep -qF 'bats-no-negated-assert.bats'
}

@test "the audit recurses, and reports a subdirectory by its relative path" {
  # Through cnb_audit and not through a second copy of its find expression. An
  # assertion that retypes the command it means to check holds a copy: the
  # function can lose its recursion with the copy still passing, and then the
  # audit returns empty for a subdirectory and empty reads as a clean tree --
  # this file's own subject, one level up.
  #
  # hack/e2e-apps/ is what makes recursion load-bearing: BATS_UNIT_FILES is a
  # non-recursive wildcard, so that directory is the one place a negated
  # assertion can live where `make unit-tests` never looks. The proof is a
  # fixture rather than that directory, which is emptying as suites move to
  # Chainsaw -- pinning against it would turn this test red for a reason that
  # has nothing to do with recursion.
  dir="$(mktemp -d)"
  mkdir -p "$dir/sub"
  cnb_new_fixture "$dir/sub"
  cnb_append "$dir/sub" '@test "a" {\n'
  cnb_append "$dir/sub" '  %s true\n' '!'
  cnb_append "$dir/sub" '}\n'

  report="$(cnb_audit "$dir")"
  # One report line and no more. An EMPTY report also satisfies this, because
  # `printf '%s\n' ""` emits a newline -- the grep below is what rejects it,
  # and that division of labour is the same in the continuation test.
  [ "$(printf '%s\n' "$report" | wc -l)" -eq 1 ]
  # Relative to the audited root, so hack/foo.bats and hack/e2e-apps/foo.bats
  # cannot report under the same name.
  printf '%s\n' "$report" | grep -q '^sub/subject.bats:3: '
  rm -rf "$dir"
}

@test "no suite states an absence in a form that cannot fail" {
  report="$(cnb_audit "$BATS_DIR")"
  if [ -n "$report" ]; then
    echo 'A statement whose first token is `!` has its status exempted from errexit,'
    echo 'so the assertion below passes whatever it finds. Spell the absence as:'
    echo '    if cmd; then echo "FAIL: <what must not have happened>"; false; fi'
    printf '%s\n' "$report"
    false
  fi
}

@test "the scan reports a negated statement in every shape measured here" {
  # Every shape below was run under the runner and reported a pass while its
  # assertion must have failed, so each is a live hole the scan has to close.
  # The name claims what this fixture holds and nothing wider: a lexical scan
  # over shell cannot promise it has enumerated the language.
  dir="$(mktemp -d)"
  cnb_new_fixture "$dir"
  cnb_append "$dir" '@test "a" {\n'
  cnb_append "$dir" '  %s true\n' '!'
  cnb_append "$dir" '  %s printf x | grep -q x\n' '!'
  cnb_append "$dir" '  echo one; %s true\n' '!'
  cnb_append "$dir" '  true && %s true\n' '!'
  cnb_append "$dir" '  false || %s true\n' '!'
  cnb_append "$dir" '  for f in a b; do\n    %s true\n  done\n' '!'
  cnb_append "$dir" '  if true; then %s true; fi\n' '!'
  # A case branch: the `)` closing the pattern is a statement boundary like any
  # other, and `case` already appears inside test bodies and stubs in this tree.
  cnb_append "$dir" '  case "$v" in\n    yes) %s true ;;\n  esac\n' '!'
  # After a background `&`, which separates statements exactly as `;` does.
  cnb_append "$dir" '  true & %s true\n' '!'
  # A brace group takes the exemption with it: the group reports the inverted
  # status as its own, so the whole thing passes. A subshell does not, which is
  # why only this one is here.
  cnb_append "$dir" '  { %s true; }\n' '!'
  # The same keywords at column zero. Every other occurrence of `then`, `do` and
  # `else` in a test body is indented, which is what made the missing space
  # before them invisible: without it the keyword opens the segment and the
  # negation after it is never a segment's first token. All three are vacuous
  # under the runner, so the miss was silent -- the direction this file says it
  # will not err in.
  cnb_append "$dir" '  for f in a b\n'
  cnb_append "$dir" 'do %s true\n' '!'
  cnb_append "$dir" '  done\n'
  cnb_append "$dir" '  if false\n'
  cnb_append "$dir" 'then %s true\n' '!'
  cnb_append "$dir" 'else %s true\n' '!'
  cnb_append "$dir" '  fi\n'
  # The same keywords with no space after the `;`. Valid shell, vacuous under
  # the runner, and missed until the separator class admitted `;` in front of a
  # keyword as well as whitespace.
  cnb_append "$dir" '  for f in a;do %s true;done\n' '!'
  cnb_append "$dir" '  if x; then :;else %s true; fi\n' '!'
  cnb_append "$dir" '}\n'

  found="$(cnb_scan "$dir/subject.bats" | wc -l)"
  [ "$found" -eq 15 ]
  rm -rf "$dir"
}

@test "the scan folds a continuation and names the line the statement opens on" {
  dir="$(mktemp -d)"
  cnb_new_fixture "$dir"
  cnb_append "$dir" '@test "a" {\n'
  # The negation has to land on the CONTINUATION line, not the line that opens
  # it. Put it on the opening line and the report says line 3 whether the fold
  # ran or not, so the assertion below passes with the folding deleted -- which
  # is the shape this whole suite exists to ban. Here folded reports 3 and
  # unfolded reports 4, so removing the fold turns the test red.
  #
  # `\\\n` and not `\\\\\n`: printf collapses each pair, so the doubled form
  # emits two backslashes -- an escaped literal, which is an ordinary argument
  # rather than a continuation. Check the bytes with `od -c` when editing this.
  cnb_append "$dir" '  echo start \\\n'
  cnb_append "$dir" '    ; %s grep -q x "$file"\n' '!'
  cnb_append "$dir" '}\n'

  report="$(cnb_scan "$dir/subject.bats")"
  # One report line and no more. An empty report satisfies this too -- one
  # newline out of printf -- so the grep below is what rules empty out, not
  # this line.
  [ "$(printf '%s\n' "$report" | wc -l)" -eq 1 ]
  printf '%s\n' "$report" | grep -q ':3: '
  rm -rf "$dir"
}

@test "the scan leaves an inverted condition and an inverted test operator alone" {
  dir="$(mktemp -d)"
  cnb_new_fixture "$dir"
  cnb_append "$dir" '@test "a" {\n'
  cnb_append "$dir" '  if %s command -v act >/dev/null; then echo none; fi\n' '!'
  cnb_append "$dir" '  if %s foo || %s bar; then echo none; fi\n' '!' '!'
  cnb_append "$dir" '  while %s ready; do sleep 1; done\n' '!'
  cnb_append "$dir" '  until %s done_yet; do sleep 1; done\n' '!'
  # The two lines above cannot tell whether `while` and `until` are in the
  # condition-strip: splitting on `;` and ` do ` already leaves `!` in the
  # middle of segment 1, so they report nothing either way. These two put the
  # negation where only the strip can save it -- drop either keyword from the
  # alternation and the line becomes a report.
  cnb_append "$dir" '  while foo && %s bar; do sleep 1; done\n' '!'
  cnb_append "$dir" '  until foo && %s bar; do sleep 1; done\n' '!'
  cnb_append "$dir" '  [ %s -f /nowhere ]\n' '!'
  cnb_append "$dir" '  [ "$a" %s= "$b" ]\n' '!'
  cnb_append "$dir" '  # a comment naming %s grep -q x file\n' '!'
  cnb_append "$dir" '}\n'

  found="$(cnb_scan "$dir/subject.bats" | wc -l)"
  [ "$found" -eq 0 ]
  # An empty report is what a scan that reads nothing returns too, so the same
  # fixture has to produce one when the offence is present.
  cnb_append "$dir" '@test "b" {\n'
  cnb_append "$dir" '  %s true\n' '!'
  cnb_append "$dir" '}\n'
  found="$(cnb_scan "$dir/subject.bats" | wc -l)"
  [ "$found" -eq 1 ]
  rm -rf "$dir"
}

@test "a negated assertion still passes under this runner whatever it finds" {
  # The premise the ban rests on, pinned rather than described: the day the
  # runner starts propagating an inverted status, this test fails and the rule
  # above can be reconsidered instead of outliving its reason.
  dir="$(mktemp -d)"
  {
    printf '@test "an assertion that must not hold" {\n'
    printf '  %s true\n' '!'
    printf '}\n'
  } > "$dir/pin.bats"

  rc=0
  "$RUNNER" "$dir/pin.bats" > "$dir/out" 2>&1 || rc=$?
  # Both halves: the runner reported success, AND a test actually ran to produce
  # it. Without the second, a fixture the runner refused to parse would satisfy
  # the first and pin nothing.
  [ "$rc" -eq 0 ]
  grep -qF 'Test OK' "$dir/out"
  rm -rf "$dir"
}

@test "the form this ban prescribes does fail, under both runners" {
  # The mirror of the test above, and the half more worth pinning. Every absence
  # converted in this tree rests on `false` inside a `then` body reaching the
  # runner as a failure. If that ever stopped -- a stray `set +e`, a change to
  # the `return 0` insertion in cozytest.sh -- the test above would still pass,
  # because a negated assertion would still be vacuous, while every converted
  # absence went silently green. The ban would then be prescribing a form that
  # checks nothing, which is the defect it exists to remove.
  #
  # Under both runners because their disagreement is the reason this class
  # exists at all: a remedy that only propagates under one of them is not a
  # remedy for the suites CI runs.
  dir="$(mktemp -d)"
  {
    printf '@test "an assertion that must fail" {\n'
    printf '  if true; then echo "FAIL: the prescribed form fired"; false; fi\n'
    printf '  echo PAST-THE-FORM\n'
    printf '}\n'
  } > "$dir/pin.bats"

  rc=0
  "$RUNNER" "$dir/pin.bats" > "$dir/out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  # It has to fail AT the form. A run that died earlier -- an unparsable
  # fixture, a missing runner -- would satisfy the status check and pin nothing.
  grep -qF 'the prescribed form fired' "$dir/out"
  if grep -qF 'PAST-THE-FORM' "$dir/out"; then
    echo "FAIL: execution continued past the prescribed form"
    false
  fi

  if command -v bats >/dev/null 2>&1; then
    rc=0
    bats "$dir/pin.bats" > "$dir/bats-out" 2>&1 || rc=$?
    [ "$rc" -ne 0 ]
    grep -qE '^not ok ' "$dir/bats-out"
  else
    echo "bats binary absent — the prescribed form NOT verified under it in this run." >&2
  fi
  rm -rf "$dir"
}

@test "neither errexit nor the ERR trap sees an inverted status" {
  # The half of the premise that says switching runners does not lift the ban.
  # Pinned at the shell rather than through the bats binary, because this is
  # where the exemption lives -- bats builds its reporting on exactly these two
  # mechanisms, so a suite host without bats installed can still hold the claim
  # honest. A shell without `bash` cannot run bats either, which is the only
  # case this skips.
  # `skip` is a bats builtin that cozytest.sh does not provide, so the note goes
  # out the way the other suites here report an unmet dependency.
  if ! command -v bash >/dev/null 2>&1; then
    echo "bash unavailable — the errexit and ERR-trap exemptions NOT verified in this run." >&2
    return 0
  fi
  # Assembled rather than written out, for the reason the header gives: spelled
  # literally, this string is a statement the scan reports against its own file.
  prog="$(printf 'set -eE; trap "echo TRAP-RAN" ERR; %s true; echo REACHED' '!')"
  out="$(bash -c "$prog" 2>&1)"
  # Reached the line below the failing assertion, and the trap never fired:
  # either alone would leave the other half unpinned.
  [ "$out" = "REACHED" ]
}
