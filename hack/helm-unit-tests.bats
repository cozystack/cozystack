#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/helm-unit-tests.sh, which decides per package directory
# whether to run `make test` and turns the results into one exit code.
#
# The script judges a package by that exit code alone, and exit 0 has two very
# different meanings. helm-unittest is fail-closed on a suite it cannot parse
# and on a suite declaring `tests: []`, but fail-open on suites that are not
# there at all: it prints "Test Suites: 0 passed, 0 total" and exits 0. So a
# chart whose suites were deleted, moved, or renamed past the `tests/*_test.yaml`
# glob reports success having asserted nothing. The other way in is the
# discovery gate: `make -C dir -n test` succeeds for a file-backed target too, so
# a path named `test` beside a package Makefile whose `test:` rule is not phony
# makes both the gate and the run exit 0 without the recipe firing.
#
# Every one of those checks runs INSIDE a directory the sweep already reached,
# so a package that never enters the loop escapes all of them at once. That is a
# separate failure, and the tests naming packages/tests are the ones covering it.
#
# The fixtures below stub `make` output rather than invoking helm, so the tests
# stay hermetic and need no plugin installed. What they exercise is the script's
# own reaction to each shape of output. The sweep resolves its package
# directories relative to the working directory, so each test runs it from a
# synthetic tree holding nothing but the packages it is about. That keeps the
# assertions exact and keeps the real repo's suites, which take minutes, out of
# a unit test.
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` at column
# 0, and injects `set -e`, so exit statuses are captured explicitly; there is no
# bats `run` or `$status`. setup()/teardown() are not honored, so each test
# creates and removes its own scratch dir inline rather than through a
# `trap ... EXIT`: that trap replaces the one the bats binary installs for its
# own bookkeeping, and a test failing under it can print no TAP line at all.
#
# Run with: hack/cozytest.sh hack/helm-unit-tests.bats
# -----------------------------------------------------------------------------

# Build a throwaway tree with one package whose `make test` prints $1 and exits
# $2. Echoes the tree root.
_tree() {
    _t=$(mktemp -d)
    mkdir -p "$_t/packages/apps/sample"
    printf 'test:\n\t@printf "%%s\\n" "%s"\n\t@exit %s\n' "$1" "$2" \
        > "$_t/packages/apps/sample/Makefile"
    printf '%s' "$_t"
}

# A package at an arbitrary path whose `test` target announces itself, so "the
# sweep reached this directory" and "the recipe actually ran" stay two separate
# observations. It also emits the line a real helm-unittest run emits, because
# the script refuses a package that reports no suite: without that line the
# fixture would be rejected for a reason unrelated to the claim under test.
make_package() {
    mkdir -p "$1"
    printf 'test:\n\t@echo %s\n\t@printf "%%s\\n" "Test Suites: 1 passed, 1 total"\n' \
        "$2" > "$1/Makefile"
}

@test "a chart reporting zero test suites fails instead of passing silently" {
    repo=$(pwd)
    t=$(_tree "Test Suites: 0 passed, 0 total" 0)

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -eq 0 ]; then
        echo "helm-unit-tests.sh exited 0 for a chart that ran no suites:" >&2
        echo "$out" >&2
        echo "a chart whose suite files vanished would report success" >&2
        rm -rf "$t"
        exit 1
    fi

    # Assert on this check's own wording, so the test cannot stay green on a
    # refusal that came from somewhere else.
    case "$out" in
        *"ran no test suites"*) ;;
        *)
            echo "refused, but not by the zero-suite check:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a shadowed test target is refused before it can no-op" {
    # `make -n test` succeeds against a file-backed target, so the discovery
    # gate cannot tell a real recipe from one make will skip. The script has to
    # notice the colliding path itself.
    repo=$(pwd)
    t=$(_tree "Test Suites: 1 passed, 1 total" 0)
    touch "$t/packages/apps/sample/test"

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -eq 0 ]; then
        echo "helm-unit-tests.sh exited 0 with a path named 'test' beside the Makefile:" >&2
        echo "$out" >&2
        echo "make would report the target up to date and skip the recipe" >&2
        rm -rf "$t"
        exit 1
    fi

    case "$out" in
        *"reported up to date"*) ;;
        *)
            echo "refused, but not by the shadowed-target check:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a colliding path is tolerated when the target is declared phony" {
    # The check has to key on what make reports, not on the path existing: a
    # package declaring `.PHONY: test` runs its recipe regardless of what sits
    # beside the Makefile, so rejecting it would be a false failure — and would
    # contradict the remedy the other check names.
    repo=$(pwd)
    t=$(mktemp -d)
    mkdir -p "$t/packages/apps/sample"
    printf '.PHONY: test\ntest:\n\t@printf "%%s\\n" "Test Suites: 1 passed, 1 total"\n' \
        > "$t/packages/apps/sample/Makefile"
    touch "$t/packages/apps/sample/test"

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -ne 0 ]; then
        echo "helm-unit-tests.sh rejected a phony target whose recipe does run:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    # Tolerating a package means it was reached and not refused. The exit status
    # alone cannot tell that from never having been reached, because the early
    # exit above the failure summary also returns 0. Written out here rather
    # than shared through a helper: cozytest.sh rewrites a function's closing
    # brace into `return 0`, so an assertion returning a status would pass
    # unconditionally under that runner.
    case "$out" in
        *"All Helm unit tests passed"*) ;;
        *)
            echo "the run never reached its summary, so nothing was evaluated:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a sub-make reporting another target up to date is not mistaken for ours" {
    # The shadowed-target check keys on make's wording, so it has to distinguish
    # our target from any other whose name merely ends in "test". A package that
    # delegates to a sub-make can legitimately print such a line while its own
    # suites run.
    repo=$(pwd)
    t=$(mktemp -d)
    mkdir -p "$t/packages/apps/sample"
    {
        printf 'test:\n'
        printf '\t@printf "%%s\\n" "make[1]: '"'"'unittest'"'"' is up to date."\n'
        printf '\t@printf "%%s\\n" "Test Suites: 2 passed, 2 total"\n'
    } > "$t/packages/apps/sample/Makefile"

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -ne 0 ]; then
        echo "helm-unit-tests.sh refused a package over an unrelated target:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    # As above: prove the package was reached, or a run that evaluated nothing
    # satisfies this test too.
    case "$out" in
        *"All Helm unit tests passed"*) ;;
        *)
            echo "the run never reached its summary, so nothing was evaluated:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a chart that runs suites still passes" {
    # Guards against the two checks above being satisfied by refusing everything.
    repo=$(pwd)
    t=$(_tree "Test Suites: 3 passed, 3 total" 0)

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -ne 0 ]; then
        echo "helm-unit-tests.sh rejected a chart that ran three suites:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    # As above: this is the test that stops the three refusals being satisfied
    # by rejecting everything, so it has to prove the chart was reached rather
    # than merely that the run returned 0.
    case "$out" in
        *"All Helm unit tests passed"*) ;;
        *)
            echo "the run never reached its summary, so nothing was evaluated:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a recipe that runs no suites at all is refused" {
    # The shape neither marker can catch. A `test:` rule whose recipe never
    # invokes helm-unittest, or that make finds nothing to do for, prints no
    # marker of any kind and exits 0, which is why the script asks for the line
    # a real run emits instead of enumerating the ways of emitting nothing.
    repo=$(pwd)
    t=$(mktemp -d)
    mkdir -p "$t/packages/apps/sample"
    printf 'test:\n\t@true\n' > "$t/packages/apps/sample/Makefile"

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -eq 0 ]; then
        echo "helm-unit-tests.sh exited 0 for a recipe that ran nothing:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    case "$out" in
        *"reported no test suite that ran"*) ;;
        *)
            echo "refused, but not by the positive-evidence check:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a package whose tests fail is still reported" {
    # Pins the exit-status branch specifically, which needs a fixture whose run
    # both exits non-zero AND prints a passing suite marker. Without the marker
    # the positive-evidence check refuses the package first, the exit status is
    # never what decided the outcome, and deleting the branch leaves this green.
    # Not a hypothetical shape: a `test` target that runs helm-unittest and then
    # a second step — a render-parity script, a vendored-version guard, go tests
    # — prints the marker and still fails, and the exit status is the only thing
    # that catches it.
    repo=$(pwd)
    t=$(_tree "Test Suites: 5 passed, 5 total" 1)

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -eq 0 ]; then
        echo "helm-unit-tests.sh exited 0 for a package whose tests failed:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    # Anchored on the summary's "  - " prefix, not the bare path: the script
    # announces "Running tests in $dir" before it runs anything, so the bare
    # path is in the output whatever the summary later does or omits.
    case "$out" in
        *"- packages/apps/sample"*) ;;
        *)
            echo "the failing package was not named in the summary:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "a package with no test target is skipped, not failed" {
    # The script must stay silent about the many packages that define no `test`
    # rule at all, or every run turns red on packages that never had suites.
    #
    # The tree carries a second package that does run a suite, and that is what
    # gives this test the ability to fail at all. With only the target-less
    # package present, `tests_found` stays 0 and the script takes its early exit
    # above the failure summary, so the run returns 0 whether or not the package
    # was recorded as failed: green for a reason that has nothing to do with the
    # claim in the title.
    repo=$(pwd)
    t=$(_tree "Test Suites: 1 passed, 1 total" 0)
    mkdir -p "$t/packages/apps/notests"
    printf 'build:\n\t@echo nothing\n' > "$t/packages/apps/notests/Makefile"

    rc=0
    out=$(cd "$t" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -ne 0 ]; then
        echo "helm-unit-tests.sh failed on a package that defines no test target:" >&2
        echo "$out" >&2
        rm -rf "$t"
        exit 1
    fi

    # Reaching the summary is the part that proves the early exit was not taken.
    # Asserting only on the exit status would pass on a run that evaluated
    # nothing at all, which is how this test was vacuous before.
    case "$out" in
        *"All Helm unit tests passed"*) ;;
        *)
            echo "the run never reached its summary, so nothing was evaluated:" >&2
            echo "$out" >&2
            rm -rf "$t"
            exit 1
            ;;
    esac

    rm -rf "$t"
}

@test "the sweep runs the suites under packages/tests" {
    # packages/tests holds cozy-lib-tests, the whole test suite for the helper
    # library that 34 charts consume by symlink. The sweep did not visit the
    # directory: the suite ran only because packages/library/cozy-lib/Makefile
    # carries a line delegating its own `test` target to it. Deleting that one
    # line took the library's coverage to zero with the sweep still green and
    # still printing "All Helm unit tests passed", because the no-suites
    # backstop was satisfied by the other package directories.
    #
    # Nothing else in the sweep could have caught it. Every check it makes — the
    # Makefile test, the `make -n test` probe, the per-package suite checks, the
    # failure collection — runs inside a directory it already visited, so a
    # package that drops out of the loop takes all of them with it and leaves
    # only the backstop, which counts suites across the whole run rather than
    # per package.
    repo=$(pwd)
    tmp=$(mktemp -d)
    make_package "$tmp/packages/tests/cozy-lib-tests" SWEEP_REACHED_TESTS

    rc=0
    out=$(cd "$tmp" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -ne 0 ]; then
        echo "the sweep refused a package under packages/tests that ran a suite:" >&2
        echo "$out" >&2
        rm -rf "$tmp"
        exit 1
    fi

    case "$out" in
        *"Running tests in packages/tests/cozy-lib-tests"*) ;;
        *)
            echo "the sweep never visited packages/tests; it printed:" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
            ;;
    esac

    # Reaching the directory and running the recipe are separate claims, so the
    # marker is asserted on its own rather than folded into the line above.
    case "$out" in
        *SWEEP_REACHED_TESTS*) ;;
        *)
            echo "the sweep announced packages/tests but never ran the recipe:" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
            ;;
    esac

    rm -rf "$tmp"
}

@test "a suite failing under packages/tests fails the sweep" {
    # Visiting the directory is worth nothing if a red suite there is swallowed:
    # the delegation line the sweep is replacing propagated the failure, and the
    # replacement has to as well.
    repo=$(pwd)
    tmp=$(mktemp -d)
    mkdir -p "$tmp/packages/tests/cozy-lib-tests"
    printf 'test:\n\t@exit 1\n' > "$tmp/packages/tests/cozy-lib-tests/Makefile"

    rc=0
    out=$(cd "$tmp" && sh "$repo/hack/helm-unit-tests.sh" 2>&1) || rc=$?

    if [ "$rc" -eq 0 ]; then
        echo "a failing suite under packages/tests left the sweep green" >&2
        echo "$out" >&2
        rm -rf "$tmp"
        exit 1
    fi

    # Anchored on the summary's "  - " prefix, for the reason given above.
    case "$out" in
        *"- packages/tests/cozy-lib-tests"*) ;;
        *)
            echo "the failing package was not named in the summary:" >&2
            echo "$out" >&2
            rm -rf "$tmp"
            exit 1
            ;;
    esac

    rm -rf "$tmp"
}
