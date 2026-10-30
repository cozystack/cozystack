#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Every file this repository hands to /bin/sh must parse under /bin/sh.
#
# Why this is not already covered. On a contributor's workstation /bin/sh is
# very often a symlink to bash -- it is on Arch, and on any distribution where
# someone repointed it -- while on the ubuntu-* runners this project builds on
# it is dash. A bash-only construct in a file whose shebang says sh therefore
# runs clean locally and is a syntax error in CI, and the local run that was
# meant to catch it is not a weaker version of CI but a different test.
#
# The failure is worse than one red test, because of how the two runners load a
# suite. hack/cozytest.sh awk-translates a .bats file into shell functions and
# DOT-SOURCES the result into its own `#!/bin/sh` process, so the whole file is
# parsed in one go before any test runs: a single `<(...)` anywhere in it is a
# parse error that kills every test in the file, not the one that contains it.
# And because bats-unit-tests stops at the first failing FILE, every suite
# alphabetically after it never runs either. One process substitution has cost
# this project a whole unit lane.
#
# What this guard therefore does is the cheapest possible version of that load:
# it parses each file with the shell that will execute it, and nothing else. No
# test runs, no cluster is touched, and the whole sweep is under two seconds --
# so the class lands here, in the unit lane, instead of inside a bats step that
# has already given up or an e2e job three hours in.
#
# The two surfaces it covers:
#
#   *.sh executed by sh   an sh shebang, or NO shebang at all -- the latter
#                         because every such file in this tree is a
#                         hack/e2e-chainsaw/_lib/ helper that a Chainsaw step
#                         dot-sources, which means it inherits that step's sh
#                         and has no shebang of its own to honour.
#   *.bats                translated exactly the way cozytest.sh translates it,
#                         because the translated text is what dash actually
#                         parses. Checking the raw file would be checking a
#                         document no shell ever sees: `@test "x" {` is not a
#                         shell construct.
#
# Two surfaces it deliberately does NOT cover, so that nobody reads a green run
# as more than it is. Makefile recipes also run under /bin/sh -- make's default
# SHELL, and this project sets none -- but a recipe line is full of `$(VAR)`
# that a shell parser reads as command substitution, so a real parse there
# yields confident nonsense. Chainsaw `script:` blocks run under `sh -c` and are
# extractable, but only with a YAML parser this suite would then depend on.
# Both are worth adding; neither is worth faking.
#
# The limit that matters most: this is a SYNTAX check, and a bashism dash parses
# happily but runs differently -- `echo -e`, `source`, `set -o pipefail`,
# `$HOSTNAME`, `$RANDOM` -- passes it. Catching those needs checkbashisms, which
# is not installed on the runners and would mean a new dependency in this lane
# for a class that has cost less than the syntax class has. `dash -n` is the
# 90% that is free.
# -----------------------------------------------------------------------------

# The directory to audit. Under the bats binary that is this file's own
# location; under cozytest.sh, which sets no BATS_TEST_FILENAME, `$0` is the
# runner -- and the answer is the same only because the runner lives in the
# directory it runs files from. The repo root is one level up. The first test
# below refuses to pass on an empty enumeration, so a wrong answer here fails
# rather than audits nothing.
SPP_HACK="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
SPP_ROOT="$(cd "$SPP_HACK/.." && pwd)"

# The parser to check with, and the honesty about which one it is.
#
# `dash` by name when it is installed, because that is what the runners execute
# and naming it removes all doubt. Otherwise `sh`, which IS dash on every
# ubuntu runner this project uses -- and is bash on a workstation, where the
# check then degrades to catching only what bash also rejects. That degradation
# is announced rather than hidden: a contributor whose sh is bash should not
# read a green line here as a promise about CI.
if command -v dash >/dev/null 2>&1; then
  SPP_SH=dash
else
  SPP_SH=sh
fi

# Reproduce cozytest.sh's translation. Kept identical to the awk program there,
# including its blunt rule that a line which is EXACTLY `}` closes a test -- a
# nested brace at column zero is rewritten too. That is the runner's behaviour,
# and a checker that quietly did something smarter would be vouching for text
# CI never parses.
spp_translate() {
  awk '
    /^@test[[:space:]]+"/ {
      line  = substr($0, index($0, "\"") + 1)
      title = substr(line, 1, index(line, "\"") - 1)
      fname = "test_"
      for (i = 1; i <= length(title); i++) {
        c = substr(title, i, 1)
        fname = fname (c ~ /[A-Za-z0-9]/ ? c : "_")
      }
      printf("%s() {\n", fname)
      print "  set -e"
      next
    }
    /^}$/ {
      print "  return 0"
      print "}"
      next
    }
    { print }
  ' "$1"
  return 0
}

# Does this .sh file get executed by sh? An sh shebang, or none at all. A bash,
# zsh or ksh shebang means the file is not this guard's business, and saying so
# by shebang is the documented way to opt out.
spp_is_sh_script() {
  _first=$(head -1 "$1")
  case "$_first" in
    '#!'*bash*|'#!'*zsh*|'#!'*ksh*) return 1 ;;
    '#!'*sh*)                       return 0 ;;
    '#!'*)                          return 1 ;;
    *)                              return 0 ;;
  esac
}

# Report every file under $1 that its own shell cannot parse, one line each, and
# print nothing when they all parse.
#
# Through stdout rather than an exit status, for the reason hack/bats-no-exit-trap.bats
# records: cozytest.sh rewrites a function's closing brace into `return 0`, so a
# status set in here is discarded before the caller can read it and the check
# would pass unconditionally.
#
# Vendored upstream charts are excluded. `make update` regenerates them verbatim
# and this project does not own their prose or their scripts; a finding there is
# not actionable in this tree.
spp_audit() {
  _tmp=$(mktemp) || return 0
  find "$1" -name charts -prune -o -name node_modules -prune -o -type f \
       \( -name '*.sh' -o -name '*.bats' \) -print | sort | while IFS= read -r _f; do
    [ -e "$_f" ] || continue
    _rel=${_f#"$1"/}
    case "$_f" in
      *.bats)
        spp_translate "$_f" > "$_tmp"
        _out=$("$SPP_SH" -n "$_tmp" 2>&1) || \
          echo "$_rel: $SPP_SH rejects the translated file: $_out"
        ;;
      *)
        spp_is_sh_script "$_f" || continue
        _out=$("$SPP_SH" -n "$_f" 2>&1) || \
          echo "$_rel: $SPP_SH rejects it: $_out"
        ;;
    esac
  done
  rm -f "$_tmp"
  return 0
}

@test "every file this repo hands to sh parses under sh" {
  # The whole point of the guard, run against the tree it governs. Everything
  # below exists to prove this test can fail.
  #
  # Counted with the enumeration spp_audit actually walks, not a second one: a
  # backstop that asks a different question can pass while the audit goes
  # vacuously green, which is the failure mode this file is about.
  files=$(find "$SPP_ROOT" -name charts -prune -o -name node_modules -prune \
            -o -type f \( -name '*.sh' -o -name '*.bats' \) -print | wc -l)
  [ "$files" -gt 0 ] || { echo "FAIL: found no sh surfaces to audit at all"; false; }

  echo "parsing $files candidate file(s) with $SPP_SH"
  if [ "$SPP_SH" = sh ]; then
    # Announced, not hidden: on a workstation where /bin/sh is bash this check
    # verifies bash syntax and proves nothing about the runners.
    echo "note: checking with /bin/sh; where that is bash this is a DEGRADED check"
  fi

  offences=$(spp_audit "$SPP_ROOT")
  if [ -n "$offences" ]; then
    echo "FAIL: files executed by /bin/sh that /bin/sh cannot parse."
    printf '%s\n' "$offences"
    echo "On the runners /bin/sh is dash. Either write the construct in POSIX"
    echo "form, or -- if the script never has to run where bash is absent --"
    echo "give it a '#!/usr/bin/env bash' shebang and say so honestly."
    false
  fi
}

@test "a process substitution in a bats file is reported" {
  # The construct that has actually cost this project a unit lane. It is a
  # syntax error under dash, and because cozytest.sh sources the whole
  # translated file, it takes every test in that file down with it.
  tmp=$(mktemp -d)
  printf '%s\n' '@test "two streams" {' '  diff <(echo a) <(echo b)' '}' \
    > "$tmp/subject.bats"

  out=$(spp_audit "$tmp")
  case "$out" in
    *"subject.bats: $SPP_SH rejects the translated file"*) ;;
    *) echo "FAIL: a process substitution in a bats file went unreported: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "an unparseable sh script is reported" {
  tmp=$(mktemp -d)
  printf '%s\n' '#!/bin/sh' 'if [ 1 -eq 1 ]; then' 'echo unterminated' > "$tmp/subject.sh"

  out=$(spp_audit "$tmp")
  case "$out" in
    *"subject.sh: $SPP_SH rejects it"*) ;;
    *) echo "FAIL: an unterminated if went unreported: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a bash-only construct in a bash-shebanged script is not reported" {
  # Declaring bash is the documented way out, and a guard that refused it would
  # be demanding POSIX from files that never promised any.
  tmp=$(mktemp -d)
  printf '%s\n' '#!/usr/bin/env bash' 'if [[ -n "$1" ]]; then :; fi' > "$tmp/subject.sh"

  out=$(spp_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: a declared-bash script was audited anyway: $out"; false; }
  rm -rf "$tmp"
}

@test "a script with no shebang is audited as sh" {
  # Every no-shebang .sh in this tree is a hack/e2e-chainsaw/_lib/ helper that a
  # Chainsaw step dot-sources, so it inherits that step's sh. Skipping them
  # would leave the largest group of them unchecked.
  tmp=$(mktemp -d)
  printf '%s\n' 'diff <(echo a) <(echo b)' > "$tmp/subject.sh"

  out=$(spp_audit "$tmp")
  case "$out" in
    *"subject.sh: $SPP_SH rejects it"*) ;;
    *) echo "FAIL: a shebangless helper went unaudited: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a clean POSIX script and a clean bats file are accepted" {
  # The other direction: a guard that fires on correct code gets switched off,
  # and then it guards nothing. `local` in particular is fine -- dash has
  # supported it for decades, and several helpers in this tree rely on it.
  tmp=$(mktemp -d)
  printf '%s\n' '#!/bin/sh' 'set -eu' 'f() { local x="$1"; printf "%s\n" "$x"; }' 'f hi' \
    > "$tmp/subject.sh"
  printf '%s\n' '@test "plain" {' '  [ 1 -eq 1 ]' '}' > "$tmp/subject.bats"

  out=$(spp_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: clean files were reported: $out"; false; }
  rm -rf "$tmp"
}

@test "a nested file is audited too and keeps its path" {
  # hack/e2e-apps/ and hack/e2e-chainsaw/_lib/ both hold surfaces a flat glob
  # would miss, and two files sharing a basename must not report under the same
  # name.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/nested"
  printf '%s\n' '#!/bin/sh' 'case x in' > "$tmp/nested/deep.sh"

  out=$(spp_audit "$tmp")
  case "$out" in
    *"nested/deep.sh: $SPP_SH rejects it"*) ;;
    *) echo "FAIL: a nested file went unaudited or lost its path: $out"; false ;;
  esac
  rm -rf "$tmp"
}

@test "a vendored chart script is not audited" {
  # `make update` regenerates packages/*/*/charts/ verbatim, so a finding in
  # there is not actionable in this tree.
  tmp=$(mktemp -d)
  mkdir -p "$tmp/charts"
  printf '%s\n' '#!/bin/sh' 'case x in' > "$tmp/charts/vendored.sh"

  out=$(spp_audit "$tmp")
  [ -z "$out" ] || { echo "FAIL: a vendored chart script was audited: $out"; false; }
  rm -rf "$tmp"
}

@test "the translation matches the one cozytest.sh performs" {
  # This guard parses translated text, so it is only as good as its agreement
  # with the runner's own awk. Rather than compare the two programs, compare
  # their output: run the real runner's translation path over a fixture and
  # diff. If cozytest.sh's translation changes, this fails loudly instead of the
  # guard silently vouching for text CI no longer parses.
  tmp=$(mktemp -d)
  printf '%s\n' '@test "a title with spaces & punctuation" {' \
                '  x=1' \
                '  if [ "$x" = 1 ]; then' \
                '    echo yes' \
                '  fi' \
                '}' > "$tmp/subject.bats"

  spp_translate "$tmp/subject.bats" > "$tmp/mine"

  # The runner's own translation, extracted from the runner rather than
  # reimplemented here: everything between its awk quotes. The closing line is
  # matched by its prefix and NOT anchored at the end -- in cozytest.sh it
  # continues `> "$TMP_SH"`, and an end-anchored pattern silently fails to close
  # the range, leaving a line of shell in the extracted awk program.
  sed -n "/^awk '\$/,/^' \"\\\$TEST_FILE\"/p" "$SPP_HACK/cozytest.sh" \
    | sed '1d;$d' > "$tmp/prog.awk"
  [ -s "$tmp/prog.awk" ] || { echo "FAIL: could not extract cozytest.sh's translator"; false; }
  awk -f "$tmp/prog.awk" "$tmp/subject.bats" | grep -v '^### ' > "$tmp/theirs"

  # cozytest.sh emits a `### title` marker line this guard has no use for; drop
  # it from their side rather than emitting one here, since the marker is the
  # runner's own bookkeeping and not part of what dash parses.
  grep -v '^### ' "$tmp/mine" > "$tmp/mine.cmp"
  diff -u "$tmp/theirs" "$tmp/mine.cmp" || {
    echo "FAIL: this guard's translation has drifted from cozytest.sh's"
    false
  }
  rm -rf "$tmp"
}
