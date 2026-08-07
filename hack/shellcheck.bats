#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/shellcheck.sh
#
# These execute hack/shellcheck.sh rather than assert that it exists. Subjects
# live in each test's own temp dir and are passed as arguments; the enumeration
# test and the last one read the tree instead.
#
# A red path is asserted on the exit status, captured into `rc`, and only then
# on the output. `! cmd` is not enough: `set -e` never fires on an inverted
# command, and a gate that printed every ERROR line while exiting 0 would
# satisfy an output grep. Measured -- with every `exit 1` in the gate turned
# into `exit 0`, the output-only form passed all thirteen tests.
#
# Each test that makes a temp dir removes it on the last line of its body
# rather than from
# an EXIT trap: a trap inside an @test replaces the one the bats binary installs
# for its own bookkeeping, and a test that then fails prints no TAP line at all.
# Both runners set -e, so a failed test leaves its dir behind to look at.
#
# Tests are self-contained -- no shared setup/teardown helpers, because
# cozytest.sh's awk parser only recognizes @test blocks and treats a bare `}` on
# its own line as the end of a test function.
#
# Run with: hack/cozytest.sh hack/shellcheck.bats
# -----------------------------------------------------------------------------

@test "a violation introduced into a clean script turns the gate red" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\necho hello\n' >"$WORK/subject.sh"
  : >"$WORK/baseline"

  # Green to start with: without this the red below would prove nothing, since a
  # gate that fails on everything fails on a mutation too.
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh"

  # SC2086. Note the variable is deliberately never assigned: shellcheck
  # constant-propagates an assignment of a literal without spaces or globs and
  # stays silent, so a mutation like `D=/tmp/x; mkdir -p $D` proves nothing.
  printf 'mkdir -p $HOME/$MUTANT\n' >>"$WORK/subject.sh"

  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'SC2086' "$run_out"
  grep -q 'NEW' "$run_out"

  rm -rf "$WORK"
}

@test "removing the violation again turns the gate green" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/subject.sh"
  : >"$WORK/baseline"

  # The precondition is asserted, not assumed: without the red half proven,
  # the green line below would pass against a gate that never fails at all.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'SC2086' "$run_out"

  printf '#!/bin/sh\nset -eu\nmkdir -p "$HOME/$MUTANT"\n' >"$WORK/subject.sh"
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh"

  rm -rf "$WORK"
}

@test "a baseline entry admits exactly the findings it records" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/subject.sh"

  # One statement, two unquoted expansions, so two findings.
  printf '%s SC2086 2\n' "$WORK/subject.sh" >"$WORK/baseline"
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh"

  rm -rf "$WORK"
}

@test "one more finding of an already baselined check still turns the gate red" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/subject.sh"
  printf '%s SC2086 2\n' "$WORK/subject.sh" >"$WORK/baseline"
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh"

  # This is what a per-file baseline would wave through: the script is already
  # listed for SC2086, so only counting catches a third one being added.
  printf 'rm -rf $MUTANT_TWO\n' >>"$WORK/subject.sh"

  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'NEW' "$run_out"
  grep -q '3 finding(s), baseline records 2' "$run_out"

  rm -rf "$WORK"
}

@test "a fixed finding leaves the baseline stale and turns the gate red" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p "$HOME/$MUTANT"\n' >"$WORK/subject.sh"
  printf '%s SC2086 2\n' "$WORK/subject.sh" >"$WORK/baseline"

  # The half that makes the list shrink. Without it a baseline entry survives
  # its own findings and becomes a standing licence to reintroduce them.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'STALE' "$run_out"
  grep -q 'make shellcheck-baseline' "$run_out"

  rm -rf "$WORK"
}

@test "a count that drops without reaching zero is still stale" {
  WORK=$(mktemp -d)
  # Two findings against a baseline recording three: one was fixed, the entry
  # survives.
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$A\n' >"$WORK/subject.sh"
  printf '%s SC2086 3\n' "$WORK/subject.sh" >"$WORK/baseline"

  # The entry survives, so the "entry vanished" half of the comparison never
  # sees this. Only the count-decreased branch does, and without it a partial
  # fix leaves a licence for every finding it did not remove.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q '^  STALE' "$run_out"
  grep -q '2 finding(s), baseline records 3' "$run_out"

  rm -rf "$WORK"
}

@test "a missing shellcheck binary fails the gate instead of skipping it" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\necho hello\n' >"$WORK/subject.sh"
  : >"$WORK/baseline"

  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BIN="$WORK/absent-shellcheck" SHELLCHECK_BASELINE="$WORK/baseline" \
    hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'shellcheck not found' "$run_out"

  rm -rf "$WORK"
}

@test "a shellcheck of the wrong version fails the gate instead of reporting against it" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\necho hello\n' >"$WORK/subject.sh"
  : >"$WORK/baseline"

  # A real executable on disk, not a shell function: the gate reaches shellcheck
  # through `command -v` and an argv exec, both of which look straight past a
  # function defined in the caller.
  cat >"$WORK/shellcheck" <<'STUBEOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "ShellCheck - shell script analysis tool"
  echo "version: 0.9.0"
  exit 0
fi
exit 0
STUBEOF
  chmod +x "$WORK/shellcheck"

  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BIN="$WORK/shellcheck" SHELLCHECK_BASELINE="$WORK/baseline" \
    hack/shellcheck.sh "$WORK/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q "found '0.9.0'" "$run_out"

  rm -rf "$WORK"
}

@test "a script shellcheck cannot analyse fails the gate rather than reading as clean" {
  WORK=$(mktemp -d)
  : >"$WORK/baseline"

  # A run that reports no drift off inputs it never looked at is a silent pass.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/absent.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'were not analysed' "$run_out"

  # And it must not be able to write a baseline shrunk by the files it skipped.
  printf 'placeholder SC2086 1\n' >"$WORK/baseline"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh --regenerate "$WORK/absent.sh" >/dev/null 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -qx 'placeholder SC2086 1' "$WORK/baseline"

  rm -rf "$WORK"
}

@test "the regenerate the gate advises produces a baseline the gate accepts" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/subject.sh"
  : >"$WORK/baseline"

  # STALE tells the reader to run make shellcheck-baseline. Nothing pinned that
  # the file it writes is one a plain run then accepts, which is the whole of
  # what the advice is worth: the negative case is covered above, where a run
  # that could not analyse its inputs must not be allowed to write at all.
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh --regenerate "$WORK/subject.sh"
  grep -q "$WORK/subject.sh SC2086 2" "$WORK/baseline"
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/subject.sh"

  rm -rf "$WORK"
}

@test "a run carrying both NEW and STALE withholds the regenerate advice" {
  WORK=$(mktemp -d)
  printf '#!/bin/sh\nset -eu\nmkdir -p "$HOME/$MUTANT"\n' >"$WORK/fixed.sh"
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/broken.sh"
  printf '%s SC2086 2\n' "$WORK/fixed.sh" >"$WORK/baseline"

  # Following the regenerate advice on a mixed run would record broken.sh's
  # findings instead of reporting them: the cure for one half launders the other.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/fixed.sh" "$WORK/broken.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q '^  NEW' "$run_out"
  grep -q '^  STALE' "$run_out"
  grep -q 'Do not regenerate yet' "$run_out"
  if grep -q 'Run: make shellcheck-baseline' "$run_out"; then
    echo "regenerate advice offered on a run that also carries NEW findings" >&2
    exit 1
  fi

  rm -rf "$WORK"
}

@test "a path containing whitespace is refused rather than recorded wrong" {
  WORK=$(mktemp -d)
  tabdir="$WORK/$(printf 'a\tdir')"
  mkdir -p "$WORK/a dir" "$tabdir"
  printf '#!/bin/sh\nset -eu\nmkdir -p $HOME/$MUTANT\n' >"$WORK/a dir/subject.sh"
  cp "$WORK/a dir/subject.sh" "$tabdir/subject.sh"
  : >"$WORK/baseline"

  # A baseline entry is three space-separated fields, so this path cannot be
  # recorded: the summary splits it and the entry names a file that does not
  # exist. Both outcomes are red; only one of them checked the file.
  run_out="$WORK/out"
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$WORK/a dir/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'cannot record' "$run_out"

  # A tab is the half a space-only guard would miss, and it is the worse half:
  # the summary splits on it, so the entry would name neither the file nor the
  # check.
  rc=0
  SHELLCHECK_BASELINE="$WORK/baseline" hack/shellcheck.sh "$tabdir/subject.sh" >"$run_out" 2>&1 || rc=$?
  [ "$rc" -ne 0 ]
  grep -q 'cannot record' "$run_out"

  rm -rf "$WORK"
}

@test "enumeration covers scripts a star dot sh glob cannot see" {
  WORK=$(mktemp -d)
  listing="$WORK/listing"
  hack/shellcheck.sh --list >"$listing"

  # The platform migrations have no suffix at all, and run-migrations.sh
  # executes them on a cluster upgrade. They are why the shebang branch exists.
  extensionless=$(grep -c -v '\.sh$' "$listing")
  [ "$extensionless" -gt 0 ]
  grep -qx 'packages/core/platform/images/migrations/migrations/1' "$listing"

  # And the mirror half, so neither branch can be dropped as redundant: these
  # sourced libraries carry no shebang and are in scope by suffix alone.
  grep -qx 'hack/lib/image-refs.sh' "$listing"
  grep -qx 'packages/core/platform/images/migrations/migrations/lib/cozystack-version.sh' "$listing"
  head -n 1 hack/lib/image-refs.sh | grep -qv '^#!'

  # Vendored upstream charts stay out: `make update` regenerates them, so a
  # finding there is not ours to carry.
  if grep -q '/charts/' "$listing"; then
    echo "vendored chart scripts entered scope" >&2
    exit 1
  fi

  rm -rf "$WORK"
}

@test "the committed baseline matches the committed tree" {
  # The gate as CI runs it. Red here means either a script grew a finding or one
  # was fixed without regenerating; both are answered by the message it prints.
  hack/shellcheck.sh
}
