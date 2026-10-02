#!/usr/bin/env bats
# Bats owns per-test parallelism; Make also schedules the non-Bats targets.

load test_helper

@test "the Bats invocation receives every discovered unit file exactly once" {
    tmp=$(mktemp -d)
    mkdir "$tmp/bin"
    printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$@" >> "$BATS_ARG_LOG"' > "$tmp/bin/bats"
    chmod +x "$tmp/bin/bats"
    expected=$(MAKEFLAGS= MAKELEVEL= make --no-print-directory -s print-bats-unit-files)
    [ -n "$expected" ]
    MAKEFLAGS= MAKELEVEL= PATH="$tmp/bin:$PATH" BATS_ARG_LOG="$tmp/args" \
        make --no-print-directory -s BATS_JOBS=4 "BATS_REPORT_DIR=$tmp/reports" bats-unit-tests
    actual=$(grep '^hack/.*\.bats$' "$tmp/args")
    [ "$actual" = "$expected" ] || {
        echo "Bats did not receive the discovered unit files exactly once" >&2
        cat "$tmp/args" >&2
        false
    }
    [ "$(sed -n '1,2p' "$tmp/args")" = "$(printf '%s\n' -j 4)" ]
    rm -rf "$tmp"
}

@test "PR workflow schedules unit and controller targets with four Make jobs and -k" {
    # -k is what lets test-controllers start when a unit-tests prerequisite
    # fails first; without it a red Bats file hides the Go controller suite.
    grep -qF 'run: make unit-tests test-controllers -j4 -k --output-sync=target' \
        .github/workflows/pull-requests.yaml || {
        echo "PR checks do not use the bounded four-slot make invocation" >&2
        exit 1
    }
    [ "$(grep -c 'run: make test-controllers' .github/workflows/pull-requests.yaml)" -eq 0 ] || {
        echo "controller tests still run in a separate sequential workflow step" >&2
        exit 1
    }
}
