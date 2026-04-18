#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cross-validation between GPU recording rules and the dashboards that consume
# them. Catches:
#
#   1. dangling references — a dashboard query mentions a recording rule name
#      that doesn't exist in gpu-recording.rules.yaml. This is the bug the
#      pre-merge review caught: gpu-efficiency.json shipped panels keyed on
#      pod:tensor_saturation:avg5m without the rule being defined, so the
#      panel showed "No data" everywhere.
#
#   2. typos in rule names — same bug class, manifested as a single-character
#      difference between rule and reference.
#
# Scope: only dashboards listed in packages/system/monitoring/dashboards-infra.list
# under the "gpu/" prefix (i.e. shipped to production), not every JSON file in
# dashboards/gpu/. Untracked drafts stay out of scope on purpose — adding one
# to dashboards-infra.list is what brings it under the test.
#
# Reverse direction (rule defined but never consumed) is intentionally NOT
# enforced: some rules exist for ad-hoc PromQL or upcoming dashboards. Treat
# unused rules as an editorial concern, not a regression.
#
# Title syntax constraints from cozytest.sh's awk parser:
#   - Titles delimited by ASCII double quotes; embedded quotes truncate.
#   - Only [A-Za-z0-9] from the title survives into the function name; titles
#     differing only in punctuation collapse to the same function.
#
# Run with: hack/cozytest.sh hack/check-gpu-recording-rules.bats
# -----------------------------------------------------------------------------

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
RULES_FILE="$REPO_ROOT/packages/system/monitoring-agents/alerts/gpu-recording.rules.yaml"
DASHBOARDS_LIST="$REPO_ROOT/packages/system/monitoring/dashboards-infra.list"
DASHBOARDS_DIR="$REPO_ROOT/dashboards"

# Extract the set of "- record: NAME" entries from the rules YAML.
# Outputs one rule name per line, sorted and deduplicated.
extract_rules() {
  awk '/^[[:space:]]*-[[:space:]]*record:[[:space:]]/ {
    sub(/^[[:space:]]*-[[:space:]]*record:[[:space:]]*/, "")
    sub(/[[:space:]]*$/, "")
    print
  }' "$RULES_FILE" | sort -u
}

# Extract the set of recording-rule references from a dashboard JSON.
# A recording-rule reference is matched by the pattern
#   <segment>:<segment>(:<segment>)+
# where each <segment> is [a-z0-9_]. Raw DCGM metrics (DCGM_FI_*),
# kube-state-metrics (kube_*) and similar uppercase / single-word metric
# names do not match because the leading segment must be lowercase and the
# whole expression must contain at least two ':' characters.
extract_refs() {
  json_file=$1
  grep -hoE '[a-z][a-z0-9_]*:[a-z0-9_]+:[a-z0-9_]+' "$json_file" | sort -u
}

# Resolve "gpu/foo" -> "$DASHBOARDS_DIR/gpu/foo.json"
list_tracked_gpu_dashboards() {
  awk '/^gpu\// { print $0 ".json" }' "$DASHBOARDS_LIST"
}

@test "every recording rule reference in tracked GPU dashboards has a matching record" {
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  extract_rules > "$TMP/rules.txt"
  [ -s "$TMP/rules.txt" ] || { echo "no recording rules extracted from $RULES_FILE" >&2; exit 1; }

  list_tracked_gpu_dashboards > "$TMP/dashboards.txt"
  [ -s "$TMP/dashboards.txt" ] || { echo "no gpu/* dashboards listed in $DASHBOARDS_LIST" >&2; exit 1; }

  failed=0
  while IFS= read -r dashboard_rel; do
    dashboard="$DASHBOARDS_DIR/$dashboard_rel"
    if [ ! -f "$dashboard" ]; then
      echo "ERROR: dashboard listed but file missing: $dashboard" >&2
      failed=1
      continue
    fi

    extract_refs "$dashboard" > "$TMP/refs.txt"
    # comm -23: lines unique to refs.txt (referenced but not defined)
    # Both inputs must be sorted; extract_* helpers already sort.
    comm -23 "$TMP/refs.txt" "$TMP/rules.txt" > "$TMP/missing.txt"
    if [ -s "$TMP/missing.txt" ]; then
      echo "ERROR: $dashboard_rel references undefined recording rules:" >&2
      sed 's/^/  - /' "$TMP/missing.txt" >&2
      failed=1
    fi
  done < "$TMP/dashboards.txt"

  [ "$failed" -eq 0 ]
}

@test "every tracked GPU dashboard listed in dashboards-infra.list exists on disk" {
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  list_tracked_gpu_dashboards > "$TMP/dashboards.txt"
  [ -s "$TMP/dashboards.txt" ] || { echo "no gpu/* dashboards listed in $DASHBOARDS_LIST" >&2; exit 1; }

  failed=0
  while IFS= read -r dashboard_rel; do
    dashboard="$DASHBOARDS_DIR/$dashboard_rel"
    if [ ! -f "$dashboard" ]; then
      echo "ERROR: $dashboard_rel listed in dashboards-infra.list but $dashboard does not exist" >&2
      failed=1
    fi
  done < "$TMP/dashboards.txt"

  [ "$failed" -eq 0 ]
}
