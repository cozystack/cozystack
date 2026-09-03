#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for the .PHONY declarations this tree depends on: the one in
# hack/package.mk, which most package Makefiles under packages/ include, and the
# root Makefile's, where one target is already shadowed by a real path.
#
# `.PHONY=a b c` assigns a variable; `.PHONY: a b c` declares targets. Only the
# second does anything, and make reports neither. The declaration lists the nine
# targets package.mk defines and stops there.
#
# `update` and `image` are deliberately absent: package.mk does not define them,
# the including package Makefile does, and naming an undefined target in .PHONY
# creates it as an empty target. That turns "No rule to make target" (exit 2)
# into "Nothing to be done" (exit 0) wherever the rule is missing, including the
# CI build matrix, which is parsed from the root Makefile `build:` target and
# runs `make -C <pkg> image` per unit. The last two tests pin the exclusion.
#
# The first test and the control below deliberately read different lists — the
# first the names on the `.PHONY:` line, the control the targets the file
# defines — and merging them back into one is the tidy-up to resist. Which list
# each takes is argued where it is taken.
#
# The first test is narrower than its name suggests, in two ways. It probes the
# names on the `.PHONY:` line, so it cannot see a name dropped from that line;
# the set-equality test is what guards every name's presence. And it decides by
# matching make's "is up to date", which make prints only for a target that has
# a recipe. A recipe-less alias never draws it: with no prerequisite both forms
# print "Nothing to be done", and with a prerequisite that is itself out of
# date, which every target here is for being phony, the working form prints that
# prerequisite's recipe while the broken form prints "Nothing to be done". The
# probe separates neither pair, so it would pass on such a name either way.
# Every target package.mk defines has a recipe, so nothing is uncovered today.
# The control test below does not close the gap either: an alias is a defined
# target, so the control probes it and requires "is up to date" from it, which
# turns the control red against the probe rather than against the alias.
# Adding one means revisiting both.
#
# The decoy files all carry one fixed timestamp, and that is load-bearing rather
# than tidy. Six of the nine targets take `check` as a prerequisite, and `apply`
# and `delete` reach it through `suspend` as well. Touch the decoys in list order
# and `check` lands newer than the targets that depend on it, so make rebuilds
# them for that reason alone and prints a recipe whatever `.PHONY` says. The
# probe then reports success against the broken form. Equal mtimes remove the
# question: nothing is ever newer than its own prerequisite, in any list order,
# so the recipe running means phony and nothing else. Measured on GNU Make 4.3,
# which is what CI runs, list order hid the bug for up to six of the nine, and
# which six moved with where the clock tick fell. On 3.81 the whole loop landed
# inside one timestamp and hid nothing, so a local run could not see it.
#
# Constraints on anyone editing this file: every make invocation is pinned to
# LC_ALL=C, because the assertions match English make wording; cozytest.sh
# recognizes only @test blocks and a bare `}` at column 0, and injects `set -e`,
# so exit statuses are captured explicitly rather than tested inline.
#
# Run with: hack/cozytest.sh hack/package-mk-phony.bats
# -----------------------------------------------------------------------------

@test "every target hack/package.mk declares phony still runs when a file of that name exists" {
    repo=$(pwd)
    targets=$(sed -n 's/^\.PHONY:[[:space:]]*//p' "$repo/hack/package.mk")

    # Without this guard the loop below is a vacuous pass against `.PHONY=`,
    # which the sed no longer matches.
    if [ -z "$targets" ]; then
        echo "no '.PHONY:' declaration found in hack/package.mk" >&2
        exit 1
    fi

    tmp=$(mktemp -d)
    printf 'include %s/hack/package.mk\n' "$repo" > "$tmp/Makefile"

    # One fixed timestamp for every decoy, so no target is ever newer than a
    # target it depends on. See the header: list order silently disarms this
    # probe for the targets that hang off `check`.
    for t in $targets; do
        touch -t 200101010000 "$tmp/$t"
    done

    for t in $targets; do
        # cozytest.sh runs with `set -e`, so capture the exit status explicitly
        # rather than letting a failing make abort before the diagnostics below.
        rc=0
        out=$(LC_ALL=C make --dry-run --directory "$tmp" "$t" 2>&1) || rc=$?
        if [ "$rc" -ne 0 ]; then
            echo "make $t exited $rc under --dry-run:" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
        fi
        case "$out" in
            *"is up to date"*)
                echo "make $t no-oped with a file named $t present:" >&2
                echo "$out" >&2
                echo "hack/package.mk does not actually declare $t phony" >&2
                rm -rf "$tmp"
                exit 1
                ;;
        esac
    done

    rm -rf "$tmp"
}

@test "the probe reports up-to-date when .PHONY is written as an assignment" {
    # Negative control: proves the probe can see a file-backed target at all.
    # The list is the targets package.mk defines, not the names on its `.PHONY:`
    # line, because "is up to date" is only a meaningful answer for a name that
    # has a rule. A declared name the file does not define prints "Nothing to be
    # done" in both forms, so probing it here fails against the probe instead of
    # against the defect; the set-equality test is what catches that name, and
    # it carries the message that says so. The list is read from the original
    # file: the copy is the mutated one.
    repo=$(pwd)
    targets=$(grep -E '^[A-Za-z0-9][A-Za-z0-9_-]*([[:space:]]+[A-Za-z0-9][A-Za-z0-9_-]*)*:' \
        "$repo/hack/package.mk" \
        | grep -vE '^[^:]*:+=' | sed 's/:.*//' \
        | tr ' ' '\n' | grep -v '^$' | sort -u)
    if [ -z "$targets" ]; then
        echo "no targets extracted from hack/package.mk — extraction broke" >&2
        exit 1
    fi

    tmp=$(mktemp -d)

    sed 's/^\.PHONY:/.PHONY=/' "$repo/hack/package.mk" > "$tmp/package.mk"
    if ! grep -q '^\.PHONY=' "$tmp/package.mk"; then
        echo "mutation to the assignment form did not apply to the copy" >&2
        rm -rf "$tmp"
        exit 1
    fi

    printf 'include %s/package.mk\n' "$tmp" > "$tmp/Makefile"

    # Same fixed timestamp as the first test, for the same reason.
    for t in $targets; do
        touch -t 200101010000 "$tmp/$t"
    done

    for t in $targets; do
        rc=0
        out=$(LC_ALL=C make --dry-run --directory "$tmp" "$t" 2>&1) || rc=$?
        if [ "$rc" -ne 0 ]; then
            echo "make $t exited $rc under --dry-run:" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
        fi
        case "$out" in
            *"is up to date"*) ;;
            *)
                echo "probe failed to detect a file-backed $t:" >&2
                echo "$out" >&2
                rm -rf "$tmp"
                exit 1
                ;;
        esac
    done

    rm -rf "$tmp"
}

@test "the .PHONY list and the targets package.mk defines are the same set" {
    # The first test only probes names already on the list, so this is the sole
    # guard on a name being dropped from it. The reverse direction catches a
    # declared-but-undefined name, which make turns into an empty target.
    # `%-update` is out of reach for both: .PHONY matches literally.
    repo=$(pwd)
    # Flattened because the `case` tests match on " name " and an embedded
    # newline makes a name fail that check and get reported wrongly.
    declared=$(sed -n 's/^\.PHONY:[[:space:]]*//p' "$repo/hack/package.mk" \
        | tr '\n' ' ')
    # A target name outside this pattern — leading underscore, dot, slash — is
    # legal in make and skipped here in silence, so keep new names inside it.
    # One rule may name several targets, so the pattern accepts a run of them
    # and the `tr` splits the run apart again; anchoring the colon to a single
    # name instead would drop every name on such a line, silently for a target
    # nothing depends on. The second grep drops assignments by the `:=` itself
    # rather than by whatever precedes it, `:+` covering `::=` and `:::=`, so
    # `export FOO:=bar` goes too — matching the name in front instead would let
    # that line through and report `export` and `FOO` as undeclared targets.
    defined=$(grep -E '^[A-Za-z0-9][A-Za-z0-9_-]*([[:space:]]+[A-Za-z0-9][A-Za-z0-9_-]*)*:' \
        "$repo/hack/package.mk" \
        | grep -vE '^[^:]*:+=' | sed 's/:.*//' \
        | tr ' ' '\n' | grep -v '^$' | sort -u | tr '\n' ' ')

    if [ -z "$declared" ] || [ -z "$defined" ]; then
        echo "could not extract targets from hack/package.mk — extraction broke" >&2
        exit 1
    fi

    missing=""
    for t in $defined; do
        case " $declared " in
            *" $t "*) ;;
            *) missing="$missing $t" ;;
        esac
    done

    if [ -n "$missing" ]; then
        echo "hack/package.mk defines these targets but does not declare them phony:$missing" >&2
        echo "a file of that name next to a package Makefile would silence the recipe" >&2
        exit 1
    fi

    undefined=""
    for t in $declared; do
        case " $defined " in
            *" $t "*) ;;
            *) undefined="$undefined $t" ;;
        esac
    done

    if [ -n "$undefined" ]; then
        echo "hack/package.mk declares these targets phony but defines none of them:$undefined" >&2
        echo "naming an undefined target in .PHONY creates it as an empty target, so the" >&2
        echo "command reports success instead of failing with 'No rule to make target'" >&2
        exit 1
    fi
}

@test "update and image stay undeclared so a package without the rule fails loudly" {
    # Pins the exclusion: a package with no `update:`/`image:` rule must still
    # fail loudly. Declaring them centrally would make every such package report
    # success having built nothing, CI included.
    repo=$(pwd)
    tmp=$(mktemp -d)

    printf 'include %s/hack/package.mk\n' "$repo" > "$tmp/Makefile"

    for t in update image; do
        rc=0
        out=$(LC_ALL=C make --directory "$tmp" "$t" 2>&1) || rc=$?
        # Exit status and diagnostic are independent observables, so both are
        # pinned. 2 is what make returns for a fatal error, and the number is
        # what a caller branches on — a wrapper or a future make that kept the
        # wording while returning something else would pass the case below.
        if [ "$rc" -ne 2 ]; then
            echo "expected 'make $t' to exit 2 in a package that defines no $t rule," >&2
            echo "but it exited $rc — is $t named in the .PHONY list?" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
        fi
        case "$out" in
            *"No rule to make target"*) ;;
            *)
                echo "expected 'make $t' to report no rule to make target, got:" >&2
                echo "$out" >&2
                rm -rf "$tmp"
                exit 1
                ;;
        esac
    done

    rm -rf "$tmp"
}

@test "adding update and image to the .PHONY list silences that failure" {
    # Negative control. Test 4 passes vacuously if its Makefile never reads
    # package.mk at all: a broken `include` path also exits non-zero with "No
    # rule to make target", naming the missing file. This mutates a copy and
    # greps that the mutation landed, so a wrong path fails here instead.
    repo=$(pwd)
    tmp=$(mktemp -d)

    sed 's/^\(\.PHONY:.*\)$/\1 update image/' "$repo/hack/package.mk" > "$tmp/package.mk"
    if ! grep -q '^\.PHONY:.* update image$' "$tmp/package.mk"; then
        echo "mutation extending the declaration did not apply to the copy" >&2
        rm -rf "$tmp"
        exit 1
    fi

    printf 'include %s/package.mk\n' "$tmp" > "$tmp/Makefile"

    for t in update image; do
        rc=0
        out=$(LC_ALL=C make --directory "$tmp" "$t" 2>&1) || rc=$?
        if [ "$rc" -ne 0 ]; then
            echo "mutation did not take effect: 'make $t' still failed with exit $rc" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
        fi
        case "$out" in
            *"Nothing to be done"*) ;;
            *)
                echo "expected the mutated copy to report nothing to be done for $t, got:" >&2
                echo "$out" >&2
                rm -rf "$tmp"
                exit 1
                ;;
        esac
    done

    rm -rf "$tmp"
}

@test "the root Makefile declares test phony so the test directory cannot shadow it" {
    # The one target in this tree that a real path already shadows. `test:` in
    # the root Makefile produces no file, so make treats the existing test/
    # directory as its product: undeclared, `make test` prints "is up to date"
    # and the recipe never runs, while docs/agents/overview.md advertises the
    # command as the way to run the full suite. Every other undeclared target
    # in that file is unshadowed today, so this is the only live one.
    #
    # Probed with --dry-run because the recipe wants a live cluster. The path is
    # what makes the probe mean anything, so its absence fails loudly rather
    # than passing quietly.
    repo=$(pwd)
    if [ ! -e "$repo/test" ]; then
        echo "no test/ path at the repo root: this probe no longer measures anything" >&2
        exit 1
    fi

    rc=0
    out=$(LC_ALL=C make --dry-run --directory "$repo" test 2>&1) || rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "make test exited $rc under --dry-run:" >&2
        echo "$out" >&2
        exit 1
    fi
    case "$out" in
        *"is up to date"*)
            echo "make test no-oped with the test/ path present:" >&2
            echo "$out" >&2
            echo "the root Makefile does not declare test phony" >&2
            exit 1
            ;;
    esac
}
