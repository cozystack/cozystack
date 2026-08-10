#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for hack/helm-unit-tests.sh
#
# cozytest.sh's awk parser recognizes only @test blocks and a bare `}` on its
# own line; there is no bats `run` or `$status`. Each test runs as a shell
# function under `set -eu -x`, so assertions are direct shell tests that exit
# non-zero on failure. setup()/teardown() are not honored — each test creates
# and cleans its own scratch dir, and cleanup is the last statement of the body
# rather than a `trap ... EXIT`: that trap replaces the one the bats binary
# installs for its own bookkeeping, and a test failing under it can print no TAP
# line at all. Last, not before the assertions, so a failure leaves the scratch
# tree behind for inspection.
#
# The sweep resolves its package directories relative to the working directory,
# so each test runs it from a synthetic tree holding nothing but the packages it
# is about. That keeps the assertions exact and keeps the real repo's suites,
# which take minutes, out of a unit test.
#
# Run with: hack/cozytest.sh hack/helm-unit-tests.bats
# -----------------------------------------------------------------------------

SWEEP="$PWD/hack/helm-unit-tests.sh"

# A package whose `test` target announces itself, so "the sweep reached this
# directory" and "the suite actually ran" are two separate observations.
make_package() {
    mkdir -p "$1"
    printf 'test:\n\t@echo %s\n' "$2" > "$1/Makefile"
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
    # Nothing else in the sweep could have caught it. Every check it makes —
    # the Makefile test, the `make -n test` probe, the failure collection —
    # runs inside a directory it already visited, so a package that drops out
    # of the loop takes all of them with it and leaves only the backstop,
    # which counts suites across the whole run rather than per package.
    tmp=$(mktemp -d)
    make_package "$tmp/packages/tests/cozy-lib-tests" SWEEP_REACHED_TESTS
    output=$(cd "$tmp" && "$SWEEP" 2>/dev/null)
    if ! printf '%s\n' "$output" | grep -q "Running tests in packages/tests/cozy-lib-tests"; then
        echo "the sweep never visited packages/tests; it printed:" >&2
        printf '%s\n' "$output" >&2
        exit 1
    fi
    printf '%s\n' "$output" | grep -q SWEEP_REACHED_TESTS
    rm -rf "$tmp"
}

@test "a suite failing under packages/tests fails the sweep" {
    # Visiting the directory is worth nothing if a red suite there is swallowed:
    # the delegation line the sweep is replacing propagated the failure, and the
    # replacement has to as well.
    tmp=$(mktemp -d)
    mkdir -p "$tmp/packages/tests/cozy-lib-tests"
    printf 'test:\n\t@exit 1\n' > "$tmp/packages/tests/cozy-lib-tests/Makefile"
    rc=0
    (cd "$tmp" && "$SWEEP" >/dev/null 2>&1) || rc=$?
    if [ "$rc" -eq 0 ]; then
        echo "a failing suite under packages/tests left the sweep green" >&2
        exit 1
    fi
    rm -rf "$tmp"
}
