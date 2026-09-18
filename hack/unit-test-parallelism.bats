#!/usr/bin/env bats
# Contract for the bounded parallel unit-test fan-out used by PR CI.

@test "every non-E2E BATS file is an independent Make prerequisite" {
    # --output-sync arrived in GNU Make 4.0, and macOS still ships 3.81, where
    # this invocation dies on an unknown option rather than telling the reader
    # why. CI is on 4.x, so the contract is still enforced where it gates a
    # merge; skipping here keeps the suite runnable on a contributor's machine
    # instead of failing for a reason that has nothing to do with the contract.
    make_major=$(make --version 2>/dev/null | sed -n '1s/^GNU Make \([0-9][0-9]*\).*/\1/p')
    if [ -z "$make_major" ] || [ "$make_major" -lt 4 ]; then
        echo "skipped: this make has no --output-sync (needs GNU Make >= 4.0)" >&2
        return 0
    fi
    expected=$(find hack -maxdepth 1 -type f -name '*.bats' ! -name 'e2e-*.bats' | wc -l)
    output=$(make -n -j4 --output-sync=target bats-unit-tests)
    # The COZYTEST_TRACE prefix is part of the pattern on purpose: the switch is
    # per-target now that the serial loop is gone, and a target that loses it
    # would go back to streaming full xtrace for every suite.
    actual=$(printf '%s\n' "$output" | grep -c '^COZYTEST_TRACE=[^ ]* hack/cozytest.sh "hack/.*\.bats"$')
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
