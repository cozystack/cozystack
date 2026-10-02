#!/usr/bin/env bats

# actions/checkout v4.4.0 and later refuse to check out a fork's pull request
# ref from a workflow_run workflow unless the step sets
# allow-unsafe-pr-checkout. e2e-fork.yaml is such a workflow, so a checkout of
# refs/pull/... there without the opt-in fails the publish job for every fork
# PR, and the required "E2E Tests" status never turns green.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
FORK="$REPO_ROOT/.github/workflows/e2e-fork.yaml"

# Prints one line per actions/checkout step: "<ref> <opt-in>", where <opt-in>
# is the value of allow-unsafe-pr-checkout or "unset".
checkout_steps() {
  awk '
    function flush() { if (in_step) print ref " " optin; in_step = 0 }
    /^[[:space:]]*- (name|uses):/ { flush() }
    /uses: actions\/checkout@/ { in_step = 1; ref = "default"; optin = "unset"; next }
    in_step && /^[[:space:]]*ref:/ { ref = $2 }
    in_step && /^[[:space:]]*allow-unsafe-pr-checkout:/ { optin = $2 }
    END { flush() }
  ' "$FORK"
}

@test "e2e-fork checks out at least one pull request ref" {
  checkout_steps | grep -q '^refs/pull/'
}

@test "every pull request checkout in e2e-fork opts in to fork code" {
  bad="$(checkout_steps | grep '^refs/pull/' | grep -v ' true$' || true)"
  if [ -n "$bad" ]; then
    echo "checkout of a pull request ref without allow-unsafe-pr-checkout: true:" >&2
    echo "$bad" >&2
    exit 1
  fi
}
