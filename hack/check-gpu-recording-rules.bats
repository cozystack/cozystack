#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cross-validation between GPU recording rules, the dashboards that consume
# them, and the DCGM Exporter metric set the cluster actually scrapes. Catches:
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
#   3. undeclared DCGM metrics — a dashboard query or recording rule mentions
#      a DCGM_FI_* metric that is neither in the upstream default CSV nor in
#      the project's custom CSV (dcgm-custom-metrics.yaml), meaning DCGM
#      Exporter would never emit it and the panel silently shows "No data".
#      Example regression: gpu-fleet.json shipped a TDP panel referencing
#      DCGM_FI_DEV_POWER_MGMT_LIMIT before the custom CSV declared it.
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
DCGM_DEFAULT_CSV="$REPO_ROOT/hack/dcgm-default-counters.csv"
DCGM_CUSTOM_CSV="$REPO_ROOT/packages/system/gpu-operator/examples/dcgm-custom-metrics.yaml"

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

# Extract the set of DCGM_FI_* metric names declared in a CSV file. Handles
# both the upstream-style default CSV (unindented) and the ConfigMap-style
# custom CSV (YAML-indented). A declaration line starts — after any leading
# whitespace — with "DCGM_FI_<NAME>," ; comment lines begin with "#" and are
# skipped. Uses POSIX awk's match()+RSTART/RLENGTH so no GNU extensions
# are required.
extract_csv_metrics() {
  file=$1
  awk '
    {
      line = $0
      sub(/^[[:space:]]+/, "", line)
      if (line ~ /^#/) next
      if (match(line, /^DCGM_FI_[A-Z0-9_]+/)) {
        print substr(line, RSTART, RLENGTH)
      }
    }
  ' "$file" | sort -u
}

# Extract the set of DCGM_FI_* metric references from a text file (dashboard
# JSON or rules YAML). A DCGM metric name has at least two underscore-delimited
# segments after the "DCGM_FI_" prefix (e.g. DCGM_FI_DEV_GPU_UTIL, DCGM_FI_PROF_
# PIPE_TENSOR_ACTIVE, DCGM_FI_DRIVER_VERSION). Requiring two segments keeps
# the matcher from latching onto glob stubs like "DCGM_FI_DEV_*_VIOLATION" that
# appear in comments.
extract_dcgm_refs() {
  file=$1
  grep -hoE 'DCGM_FI_[A-Z0-9]+(_[A-Z0-9]+)+' "$file" | sort -u
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

@test "every DCGM metric referenced in tracked dashboards and rules is declared" {
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT

  [ -f "$DCGM_DEFAULT_CSV" ] || { echo "missing $DCGM_DEFAULT_CSV" >&2; exit 1; }
  [ -f "$DCGM_CUSTOM_CSV" ]  || { echo "missing $DCGM_CUSTOM_CSV"  >&2; exit 1; }

  {
    extract_csv_metrics "$DCGM_DEFAULT_CSV"
    extract_csv_metrics "$DCGM_CUSTOM_CSV"
  } | sort -u > "$TMP/declared.txt"
  [ -s "$TMP/declared.txt" ] || { echo "no DCGM metrics extracted from CSVs" >&2; exit 1; }

  list_tracked_gpu_dashboards > "$TMP/dashboards.txt"
  [ -s "$TMP/dashboards.txt" ] || { echo "no gpu/* dashboards listed in $DASHBOARDS_LIST" >&2; exit 1; }

  failed=0

  # Dashboard coverage — every dashboard's DCGM references must resolve.
  while IFS= read -r dashboard_rel; do
    dashboard="$DASHBOARDS_DIR/$dashboard_rel"
    [ -f "$dashboard" ] || continue  # handled by the existence test
    extract_dcgm_refs "$dashboard" > "$TMP/refs.txt"
    [ -s "$TMP/refs.txt" ] || continue  # dashboard relies entirely on recording rules
    comm -23 "$TMP/refs.txt" "$TMP/declared.txt" > "$TMP/missing.txt"
    if [ -s "$TMP/missing.txt" ]; then
      echo "ERROR: $dashboard_rel references DCGM metrics not declared in any CSV:" >&2
      sed 's/^/  - /' "$TMP/missing.txt" >&2
      failed=1
    fi
  done < "$TMP/dashboards.txt"

  # Rules coverage — recording rules consume DCGM directly, so their set
  # must be declared too, otherwise derived series on every dashboard
  # collapse to empty.
  extract_dcgm_refs "$RULES_FILE" > "$TMP/rule-refs.txt"
  if [ -s "$TMP/rule-refs.txt" ]; then
    comm -23 "$TMP/rule-refs.txt" "$TMP/declared.txt" > "$TMP/rule-missing.txt"
    if [ -s "$TMP/rule-missing.txt" ]; then
      echo "ERROR: gpu-recording.rules.yaml references DCGM metrics not declared in any CSV:" >&2
      sed 's/^/  - /' "$TMP/rule-missing.txt" >&2
      failed=1
    fi
  fi

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
