#!/usr/bin/env bats
# Contract for how the unit-test job gets `helm unittest`.
#
# The plugin is not on the runner image, so the job has to obtain it, and the
# two ways of getting that wrong are opposites. Fetch it unpinned and an
# upstream release silently re-points the unit-test runner for every pull
# request. Fetch it on every run and a rate-limited GitHub release download
# fails a required check for a reason that has nothing to do with the tree,
# which is what run 34131816440 was: 92 bytes in 11 seconds, a checksum
# rejection, and a dead step before the first test.
#
# So the contract has three parts, and none of them substitutes for another: a
# pin, a cache to keep the steady state off the network, and a retry for the
# cold path a cache miss leaves. Each is pinned here because each reads like
# removable boilerplate on its own.

WORKFLOW="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)/.github/workflows/pull-requests.yaml"

@test "the helm-unittest install is version-pinned" {
    [ -f "$WORKFLOW" ]

    # `helm plugin install` with no constraint installs the newest release --
    # its own --version help says exactly that, and the install hook logs
    # `No version found` on the way there.
    grep -q -- '--version "\${HELM_UNITTEST_VERSION}"' "$WORKFLOW" || {
        echo "the plugin install carries no --version constraint, so it tracks" >&2
        echo "whatever upstream released last and the toolchain moves without a" >&2
        echo "diff in this repository." >&2
        exit 1
    }
}

@test "one pin feeds both the cache key and the install" {
    [ -f "$WORKFLOW" ]

    # A literal version in the cache key and a variable in the install (or two
    # literals) can disagree, and the failure is a cache hit that restores the
    # wrong binary while the log claims the pinned one. Both sides must read the
    # same name, and it must be declared once.
    declared="$(grep -c '^      HELM_UNITTEST_VERSION: ' "$WORKFLOW" || true)"
    [ "${declared:-0}" -eq 1 ] || {
        echo "expected exactly one HELM_UNITTEST_VERSION declaration, found ${declared:-0}" >&2
        exit 1
    }

    grep -q 'key: helm-unittest-.*env\.HELM_UNITTEST_VERSION' "$WORKFLOW" || {
        echo "the cache key does not derive from HELM_UNITTEST_VERSION, so a" >&2
        echo "version bump can hit a cache entry built for another version." >&2
        exit 1
    }
}

@test "a throttled download is retried rather than failing the job" {
    [ -f "$WORKFLOW" ]

    # The cold path still has to fetch, and one throttled response must not
    # take a required check with it.
    grep -q 'for attempt in 1 2 3; do' "$WORKFLOW" || {
        echo "the plugin install is one-shot; a single rate-limited response" >&2
        echo "fails the whole unit-test job before any test runs." >&2
        exit 1
    }

    # A half-written plugin directory makes the next attempt fail as "plugin
    # already exists" instead of retrying the download, which turns the retry
    # into two wasted seconds.
    grep -q 'helm plugin uninstall unittest' "$WORKFLOW" || {
        echo "the retry does not clear a partial install first, so attempts 2" >&2
        echo "and 3 cannot succeed." >&2
        exit 1
    }
}
