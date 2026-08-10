#!/bin/sh
set -eu

# Script to run unit tests for all Helm charts.
# It iterates through directories in packages/apps, packages/core,
# packages/extra, packages/system, packages/library and packages/tests and runs
# the 'test' Makefile target if it exists. Keep this list in step with the loop
# below: packages/core carries suites of its own, so dropping it to match a
# stale comment would silently stop running them.
#
# packages/tests is on the list because every check below runs INSIDE a
# directory the loop already reached — the Makefile test, the `make -n test`
# probe, the failure collection. A package that never enters the loop takes all
# of them with it and leaves only tests_found, which counts suites across the
# whole run rather than per package, so it stays satisfied by the other
# directories. That is what happened here, silently:
# packages/tests/cozy-lib-tests ran only via a delegating 'test' target in
# packages/library/cozy-lib/Makefile, and deleting that line dropped the
# library's entire coverage while this sweep stayed green and still reported
# success. The delegation can stay for `make -C packages/library/cozy-lib test`
# ergonomics; it is no longer the only thing running the suite. The cost of
# keeping it is that a full sweep executes cozy-lib-tests twice, once through
# each route — 0.1s to 0.3s over six runs — so the duplicate pair of
# `Running tests in ...` lines in the log is expected rather than a bug.

FAILED_DIRS_FILE="$(mktemp)"
trap 'rm -f "$FAILED_DIRS_FILE"' EXIT

tests_found=0

check_and_run_test() {
    dir="$1"
    makefile="$dir/Makefile"

    if [ ! -f "$makefile" ]; then
        return 0
    fi

    if make -C "$dir" -n test >/dev/null 2>&1; then
        echo "Running tests in $dir"
        tests_found=$((tests_found + 1))
        if ! make -C "$dir" test; then
            printf '%s\n' "$dir" >> "$FAILED_DIRS_FILE"
            return 1
        fi
    fi

    return 0
}

for package_dir in packages/apps packages/core packages/extra packages/system packages/library packages/tests; do
    if [ ! -d "$package_dir" ]; then
        echo "Warning: Directory $package_dir does not exist, skipping..." >&2
        continue
    fi

    for dir in "$package_dir"/*; do
        [ -d "$dir" ] || continue
        check_and_run_test "$dir" || true
    done
done

if [ "$tests_found" -eq 0 ]; then
    echo "No directories with 'test' Makefile targets found."
    exit 0
fi

if [ -s "$FAILED_DIRS_FILE" ]; then
    echo "ERROR: Tests failed in the following directories:" >&2
    while IFS= read -r dir; do
        echo "  - $dir" >&2
    done < "$FAILED_DIRS_FILE"
    exit 1
fi

echo "All Helm unit tests passed."