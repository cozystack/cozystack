#!/bin/sh
# -----------------------------------------------------------------------------
# e2e-wait-hr-ready.sh — the platform install/upgrade gate.
#
# Fail unless EVERY HelmRelease in EVERY namespace reaches Ready=True. This is
# the single source of truth for the "install gate must have teeth" convention
# (docs/agents/e2e-testing.md #5): a toothless gate once shipped a permanently
# failing platform HR through green CI for weeks.
#
# Extracted from hack/e2e-install-cozystack.bats so the normal install AND the
# upgrade lane (hack/e2e-upgrade-*.bats) share one legible, fail-fast gate:
#   - `kubectl wait hr --all -A` covers every HR present when the wait starts;
#   - on any timeout we re-list and dump the FULL Ready-condition message per
#     non-Ready HR (kubectl's STATUS column truncates it), so the real error
#     (e.g. a rejected CRD) is in the test log, not only in the cozyreport;
#   - exit 1 on any non-Ready HR.
#
# Usage: e2e-wait-hr-ready.sh [timeout]   (default 15m)
# Runs under /bin/sh (dash on Ubuntu CI) — no bashisms, no pipefail.
# -----------------------------------------------------------------------------
set -eu

TIMEOUT="${1:-15m}"

if kubectl wait hr --all -A --timeout="$TIMEOUT" --for=condition=ready; then
  exit 0
fi

# The wait timed out on at least one HR. Re-list (covers HRs created after the
# wait began) and surface the real reason per non-Ready HR.
kubectl get hr -A || true
# Column 4 of `kubectl get hr -A --no-headers` is READY (NAMESPACE NAME AGE READY STATUS).
kubectl get hr -A --no-headers | awk '$4 != "True"' | while read -r ns name _; do
  echo "--- Non-ready HelmRelease: $ns/$name" >&2
  kubectl get hr -n "$ns" "$name" \
    -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' >&2 || true
done
echo "Some HelmReleases failed to reconcile" >&2
exit 1
