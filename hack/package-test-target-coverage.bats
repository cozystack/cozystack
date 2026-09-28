#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Coverage invariant for the helm-unittest sweep: every package that ships a
# suite must answer the probe the sweep opens with.
#
# hack/helm-unit-tests.sh decides whether to enter a package directory by asking
# `make -C "$dir" -n test`, and a package that answers "No rule to make target"
# is skipped without a word. Every other guard in that script — the shadowed
# target check, the zero-suite check, the positive-evidence check — runs inside
# a directory the sweep already reached, so a package missing the target escapes
# all of them at once and the sweep still reports success. The suites under
# tests/ then read as coverage to anyone opening the directory while nothing
# executes them, which is worse than having no suite at all: a suite that has
# never run is where a stale assertion hides. packages/system/gpu-operator sat
# in exactly that state, with three passing test cases nothing ever ran.
#
# hack/helm-unit-tests.bats covers the script's own reactions against synthetic
# trees. This file makes the complementary claim, about the real tree: every
# package that ships a suite answers the probe the sweep opens with.
#
# That is narrower than "the sweep reaches every suite", and deliberately so.
# Reach also depends on the hardcoded root list the sweep loops over, so
# dropping packages/system from it would take every package under that root out
# of the run while each one keeps its target and this file stays green. That is
# the same shape as the packages/tests incident recorded in the sweep's own
# header, and it wants its own guard rather than a looser predicate here.
#
# The predicate is "holds at least one tests/*_test.yaml", not "has a tests/
# directory". That glob is helm-unittest's own default, so a package outside it
# has nothing of its own for a `test:` target to run and the sweep would reject
# it for reporting zero suites. packages/system/nfs-driver is the live case:
# its tests/ holds a PersistentVolumeClaim and a StorageClass to apply by hand,
# not a suite.
#
# "Of its own" is the load-bearing part. `--with-subchart` defaults to true, so
# a package carrying no suite of its own would still run any that came down
# with a vendored chart under charts/, and this predicate would not look for it.
# Only ingress-nginx and reloader vendor such suites today and both ship their
# own as well, so nothing is out of reach; a package that vendored suites and
# wrote none itself would need this predicate widened.
#
# Probed with `make -n` rather than by grepping the Makefile for `test:`, so the
# question asked here is the same one the sweep asks — a target reached through
# an include counts, and so would a rule this file cannot parse. The sweep's
# other precondition, that `$dir/Makefile` exists, is mirrored rather than left
# to make, which is looser about the name and about there being a makefile at
# all.
#
# The verdict comes from the sweep's command unchanged. Reading the diagnostic
# needs `LC_ALL=C` and `--no-print-directory`, and both are visible to a
# Makefile through `LC_ALL` and `MAKEFLAGS`, so a Makefile branching on either
# would answer this file and the sweep differently. That is why the failing
# package is probed a second time to explain rather than once to do both.
#
# Answering the probe is not the same as declaring a recipe: a path named `test`
# beside the Makefile, or an implicit rule that can build one, satisfies it too,
# and this file would report that package as covered. That is deliberate rather
# than overlooked. It is the sweep's own gate, so a package passing here is a
# package the sweep enters once it walks that package's root, which is the
# whole claim and no more than it: the root list is the caveat above. From
# inside, the sweep's positive-evidence check refuses whatever does not print
# the line a helm-unittest run emits. A pattern rule written to run
# helm-unittest would satisfy that check on its merits, which is the right
# outcome rather than a hole: the suites would be running.
#
# Not the "is up to date" check, which is the near miss worth naming. A path
# named `test` with no rule behind it makes make print "Nothing to be done for
# 'test'." and exit 0, which that grep does not match; measured on 3.81 and
# 4.4.1. "is up to date" is what make prints for the third case, a real recipe
# shadowed by a file of that name -- the one `.PHONY: test` prevents.
#
# Runs under hack/cozytest.sh, which is POSIX /bin/sh: no arrays, no bats `run`,
# no `$status`.
# -----------------------------------------------------------------------------

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "every package shipping a helm-unittest suite answers the sweep probe" {
    # An empty violation list means "no violations" only if the scan reached
    # some packages. A renamed directory or a wrong root yields the same empty
    # string, and the test would pass having read nothing.
    scanned=0
    # cozytest.sh injects `set -u`, so every variable the loop reads is seeded
    # here rather than on first append.
    seen=""
    diagnostics=""

    for suite in "$REPO_ROOT"/packages/*/*/tests/*_test.yaml; do
        [ -f "$suite" ] || continue
        pkg=$(dirname "$(dirname "$suite")")
        # One package may ship several suites; count and probe it once.
        case " $seen " in
            *" $pkg "*) continue ;;
        esac
        seen="$seen $pkg"
        scanned=$((scanned + 1))

        # The sweep opens with `[ -f "$dir/Makefile" ]` and returns before it
        # probes anything, so mirror that first. make would not: it also accepts
        # GNUmakefile and makefile, and with no makefile at all it still answers
        # 0 for a target a path of that name satisfies. Either shape would pass
        # the probe below while the sweep skipped the package, which is the one
        # thing this file claims cannot happen.
        if [ ! -f "$pkg/Makefile" ]; then
            diagnostics="$diagnostics
  ${pkg#"$REPO_ROOT"/}: no Makefile, so the sweep returns before probing"
            continue
        fi

        # The verdict is taken from the sweep's own command, byte for byte, so
        # that no flag of this file's can separate the two answers. Deciding and
        # explaining are split for that reason: anything added here to make the
        # output readable is a variable a Makefile could branch on.
        if ! make -C "$pkg" -n test >/dev/null 2>&1; then
            # Only now, and only to explain. LC_ALL=C keeps make's wording in
            # the language the remedy text below is written against.
            #
            # --no-print-directory because on modern GNU make `-C` auto-enables
            # `-w`, and the fatal path prints the "Leaving directory" line after
            # the error, so `tail -n 1` would report the directory trace and
            # drop every diagnostic this block exists to surface: a missing rule
            # and a broken include collapse to the same trace line. Measured on
            # 4.4.1, where it happens, and on 3.81 -- still the /usr/bin/make on
            # macOS -- where `-C` prints no trace at all, so a local run cannot
            # see the difference. The version that draws the line was not
            # measured.
            #
            # `|| true` because this re-run is expected to fail and cozytest.sh
            # injects `set -e`; the verdict is already decided above.
            probe=$(LC_ALL=C make --no-print-directory -C "$pkg" -n test 2>&1 || true)

            # A missing rule is the expected cause, but an unparseable Makefile
            # and a broken include fail the same probe and have a different
            # remedy. Report what make said rather than asserting the cause.
            diagnostics="$diagnostics
  ${pkg#"$REPO_ROOT"/}: $(printf '%s' "$probe" | tail -n 1)"
        fi
    done

    if [ "$scanned" -eq 0 ]; then
        echo "no tests/*_test.yaml found under $REPO_ROOT/packages -- guard is blind." >&2
        return 1
    fi

    if [ -n "$diagnostics" ]; then
        echo "Packages shipping a helm-unittest suite the sweep cannot reach:" >&2
        # Each entry is appended with a leading newline, so the accumulator
        # opens with an empty one; drop it rather than the reader parsing a
        # blank first line under pressure.
        printf '%s\n' "$diagnostics" | sed '/^$/d' >&2
        echo "hack/helm-unit-tests.sh probes 'make -C <pkg> -n test' to decide whether" >&2
        echo "to enter a package, so these suites never run and the sweep stays green." >&2
        echo "Where the reason is a missing rule, add to that Makefile:" >&2
        echo "    .PHONY: test" >&2
        echo "    test:" >&2
        echo "    	helm unittest ." >&2
        return 1
    fi
}
