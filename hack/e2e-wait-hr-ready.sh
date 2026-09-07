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
# EXPECTED_NOT_READY_FILE (optional) names a file of releases already known to
# be not-Ready, one `namespace/name` per line, rest of the line free text. Only
# the upgrade lane sets it. Unset — the install path — this script behaves
# exactly as it always did, down to making no extra call.
#
# Why the upgrade lane needs it, and why it is a ledger rather than a bump in
# the timeout or a suite exclusion. That lane is advisory, and an advisory check
# that is red for a reason everyone already knows trains readers to ignore it;
# the day it catches something new, nobody looks. So the known set is written
# down, the gate accepts exactly that set, and anything else still fails.
#
# The entries have to expire on their own or the file becomes the very thing it
# was meant to prevent — a stale explanation that outlives its defect by a
# release or two. Two rules do that, and both are failures rather than warnings:
# an entry whose release is Ready again fails the gate until it is deleted, and
# an entry naming no release in the cluster fails it too. A ledger that cannot
# be wrong about the present is worth reading; one that can is worse than none.
#
# Usage: e2e-wait-hr-ready.sh [timeout]   (default 15m)
# Runs under /bin/sh (dash on Ubuntu CI) — no bashisms, no pipefail.
# -----------------------------------------------------------------------------
set -eu

TIMEOUT="${1:-15m}"
EXPECTED_FILE="${EXPECTED_NOT_READY_FILE:-}"

# Print the full Ready-condition message per named release; kubectl's STATUS
# column truncates it, and the real error (a rejected CRD, a failed upgrade)
# lives in the part it cuts.
dump_refs() {
  while IFS= read -r _ref; do
    [ -n "$_ref" ] || continue
    echo "--- Non-ready HelmRelease: $_ref" >&2
    kubectl get hr -n "${_ref%%/*}" "${_ref#*/}" \
      -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' >&2 || true
  done < "$1"
}

wait_rc=0
kubectl wait hr --all -A --timeout="$TIMEOUT" --for=condition=ready || wait_rc=1

# ── Install path: unchanged, including the absence of a second listing call on
# the happy path.
if [ -z "$EXPECTED_FILE" ]; then
  if [ "$wait_rc" -eq 0 ]; then
    exit 0
  fi
  # The wait timed out on at least one HR. Re-list (covers HRs created after the
  # wait began) and surface the real reason per non-Ready HR.
  kubectl get hr -A || true
  kubectl get hr -A --no-headers | awk '$4 != "True"' | while read -r ns name _; do
    echo "--- Non-ready HelmRelease: $ns/$name" >&2
    kubectl get hr -n "$ns" "$name" \
      -o jsonpath='{range .status.conditions[*]}{.type}={.status} reason={.reason}: {.message}{"\n"}{end}' >&2 || true
  done
  echo "Some HelmReleases failed to reconcile" >&2
  exit 1
fi

# ── Ledger path. A named file that cannot be read is a failure, not an empty
# expectation: the whole point is that the accepted set is explicit, and
# treating an unreadable file as "nothing expected" would turn a typo into a
# gate that quietly accepts more than it says.
if [ ! -f "$EXPECTED_FILE" ]; then
  echo "FAIL: EXPECTED_NOT_READY_FILE=$EXPECTED_FILE is not a readable file" >&2
  exit 1
fi

tmp=$(mktemp -d)

# The listing goes through a file, and not into a pipeline: a pipeline takes the
# exit status of its LAST command, so a failed `kubectl get` would leave an
# empty list and every check below would pass having compared nothing.
kubectl get hr -A --no-headers > "$tmp/hr"
# Column 4 of `kubectl get hr -A --no-headers` is READY (NAMESPACE NAME AGE
# READY STATUS).
awk '$4 == "True" { print $1"/"$2 }' "$tmp/hr" | sort -u > "$tmp/ready"
awk '$4 != "True" { print $1"/"$2 }' "$tmp/hr" | sort -u > "$tmp/notready"
awk '{ print $1"/"$2 }'              "$tmp/hr" | sort -u > "$tmp/all"

# Full-line comments only. A trailing `#` is not a comment marker here because
# the free text after the reference is expected to carry issue URLs, which
# contain one.
grep -v '^[[:space:]]*#' "$EXPECTED_FILE" | awk 'NF { print $1 }' | sort -u > "$tmp/expected" || true

comm -23 "$tmp/notready" "$tmp/expected" > "$tmp/unexpected"
comm -12 "$tmp/expected" "$tmp/ready"    > "$tmp/came_back"
comm -23 "$tmp/expected" "$tmp/all"      > "$tmp/absent"

rc=0
if [ -s "$tmp/unexpected" ]; then
  echo "FAIL: HelmRelease(s) not Ready and not recorded in $EXPECTED_FILE:" >&2
  cat "$tmp/unexpected" >&2
  dump_refs "$tmp/unexpected"
  rc=1
fi
if [ -s "$tmp/came_back" ]; then
  echo "FAIL: recorded as expected-not-Ready but Ready now — delete these entries from $EXPECTED_FILE:" >&2
  cat "$tmp/came_back" >&2
  rc=1
fi
if [ -s "$tmp/absent" ]; then
  echo "FAIL: recorded in $EXPECTED_FILE but no such HelmRelease in the cluster — delete or correct these entries:" >&2
  cat "$tmp/absent" >&2
  rc=1
fi

if [ "$rc" -eq 0 ] && [ -s "$tmp/notready" ]; then
  echo "::warning::upgrade lane accepted known not-Ready HelmRelease(s); see $EXPECTED_FILE" >&2
  echo "=== expected not-Ready after upgrade — recorded defects, NOT a healthy platform ===" >&2
  grep -v '^[[:space:]]*#' "$EXPECTED_FILE" | awk 'NF' >&2 || true
  dump_refs "$tmp/notready"
fi

rm -rf "$tmp"
exit "$rc"
