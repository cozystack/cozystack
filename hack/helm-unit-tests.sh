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
# probe, the per-package suite checks, the failure collection. A package that
# never enters the loop takes all of them with it and leaves only tests_found,
# which counts suites across the whole run rather than per package, so it stays
# satisfied by the other directories. That is what happened here, silently:
# packages/tests/cozy-lib-tests ran only via a delegating 'test' target in
# packages/library/cozy-lib/Makefile, and deleting that line dropped the
# library's entire coverage while this sweep stayed green and still reported
# success. The delegation can stay for `make -C packages/library/cozy-lib test`
# ergonomics; it is no longer the only thing running the suite. The cost of
# keeping it is that a full sweep executes cozy-lib-tests twice, once through
# each route — 0.1s to 0.3s over six runs — so the duplicate pair of
# `Running tests in ...` lines in the log is expected rather than a bug.

FAILED_DIRS_FILE="$(mktemp)"
OUTPUT_FILE="$(mktemp)"
trap 'rm -f "$FAILED_DIRS_FILE" "$OUTPUT_FILE"' EXIT

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

        # LC_ALL=C because all three checks below match English wording, one on
        # make's and two on helm-unittest's, and a localized make would disarm
        # the first of them silently, which is the failure mode this whole guard
        # exists to remove. It reaches every package's recipe, not just make, so
        # the recipes run under a fixed locale too; that is the determinism-safe
        # direction and the tree is green under it.
        #
        # Capturing to a file rather than streaming is deliberate: the checks
        # below have to read the whole output, and a pipeline's exit status in
        # POSIX sh reports the last command rather than make. The cost is that a
        # package killed mid-run prints nothing, where a stream would have shown
        # the partial run, and that make's stderr is folded into stdout.
        rc=0
        LC_ALL=C make -C "$dir" test > "$OUTPUT_FILE" 2>&1 || rc=$?
        cat "$OUTPUT_FILE"

        if [ "$rc" -ne 0 ]; then
            printf '%s\n' "$dir" >> "$FAILED_DIRS_FILE"
            return 1
        fi

        # The discovery gate above succeeds against a file-backed target too, so
        # it cannot tell a real recipe from one make will skip. Match what make
        # actually reports rather than looking for a colliding path, because a
        # package that declares the target phony runs its recipe regardless of
        # what sits next to the Makefile. The quoting around the target name
        # differs between make 3.x and 4.x, so accept either opening quote;
        # anchoring on the trailing quote alone would also match a sub-make
        # reporting some other target whose name ends in "test".
        if grep -qE "[\`']test' is up to date" "$OUTPUT_FILE"; then
            echo "ERROR: a 'test' target was reported up to date while testing" >&2
            echo "       $dir, so its recipe was skipped. The target is shadowed by" >&2
            echo "       a file or directory of that name, here or in a sub-make" >&2
            echo "       this package invokes. Remove the path, or declare the" >&2
            echo "       target phony in the Makefile that defines it." >&2
            printf '%s\n' "$dir" >> "$FAILED_DIRS_FILE"
            return 1
        fi

        # helm-unittest is fail-closed on a suite it cannot parse and on one
        # declaring `tests: []`, but fail-open on suites that are absent: it
        # reports zero of them and exits 0. Charts whose suites were deleted,
        # moved, or renamed past the tests/*_test.yaml glob land here.
        if grep -qE 'Test Suites:[[:space:]]+0 passed,[[:space:]]+0 total' "$OUTPUT_FILE"; then
            echo "ERROR: $dir ran no test suites. Its Makefile declares a 'test'" >&2
            echo "       target, so suite files are expected under tests/ matching" >&2
            echo "       *_test.yaml; helm-unittest exits 0 when it finds none." >&2
            printf '%s\n' "$dir" >> "$FAILED_DIRS_FILE"
            return 1
        fi

        # Then require positive evidence, because the two checks above match
        # shapes of running nothing and there is no reason to believe that list
        # is complete. A recipe that never invokes helm-unittest, and a `test:`
        # rule make finds nothing to do for, both print no marker at all, so no
        # further match could catch them. Asking instead for the line a real run
        # always emits turns the question round: every way of asserting nothing
        # exits 0, so the run has to say what it asserted. Kept after the two
        # specific checks, which name a cause and a remedy where they apply,
        # rather than replacing them.
        #
        # The tradeoff is that a `test` target which legitimately runs no
        # helm-unittest would have to be renamed or given an opt-out. That is
        # the same bargain the zero-suite check already strikes, and no package
        # is in that position today.
        if ! grep -qE 'Test Suites:[[:space:]]+[1-9][0-9]* passed' "$OUTPUT_FILE"; then
            echo "ERROR: $dir reported no test suite that ran. Its Makefile" >&2
            echo "       declares a 'test' target, so its recipe is expected to" >&2
            echo "       run helm-unittest over suites under tests/ matching" >&2
            echo "       *_test.yaml. A recipe that runs something else, and a" >&2
            echo "       rule make had nothing to do for, both exit 0 in silence." >&2
            echo "       If every package fails this way at once, suspect the" >&2
            echo "       summary wording instead: this matches helm-unittest's" >&2
            echo "       own, and a plugin upgrade can change it." >&2
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