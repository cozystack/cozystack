#!/usr/bin/env bats
# Contract for the bounded parallel unit-test fan-out used by PR CI.

@test "every non-E2E BATS file is an independent Make prerequisite" {
    expected=$(find hack -maxdepth 1 -type f -name '*.bats' ! -name 'e2e-*.bats' | wc -l)
    output=$(make -n -j4 --output-sync=target bats-unit-tests)
    actual=$(printf '%s\n' "$output" | grep -c '^hack/cozytest.sh "hack/.*\.bats"$')
    [ "$actual" -eq "$expected" ] || {
        echo "make scheduled $actual BATS files independently, expected $expected" >&2
        exit 1
    }
    if printf '%s\n' "$output" | grep -q 'for f in'; then
        echo "bats-unit-tests still serializes the suite in a shell loop" >&2
        exit 1
    fi
}

@test "PR workflow shares four slots across unit and controller targets" {
    grep -qF 'run: make unit-tests test-controllers -j4 --output-sync=target' \
        .github/workflows/pull-requests.yaml || {
        echo "PR checks do not use the bounded four-slot make invocation" >&2
        exit 1
    }
    [ "$(grep -c 'run: make test-controllers' .github/workflows/pull-requests.yaml)" -eq 0 ] || {
        echo "controller tests still run in a separate sequential workflow step" >&2
        exit 1
    }
}
