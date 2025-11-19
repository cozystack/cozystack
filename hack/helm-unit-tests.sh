#!/bin/sh
set -eu

# Script to run unit tests for all Helm charts.
# It iterates through directories in packages/apps, packages/extra,
# packages/system, and packages/library and runs the 'test' Makefile
# target if it exists.

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

for package_dir in packages/apps packages/extra packages/system packages/library; do
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