#!/usr/bin/env bats

ALERT_SEVERITY_ALLOWED=" security critical major minor warning indeterminate informational normal ok cleared debug trace unknown "

alert_severity_rows() {
  severity_root=${ALERT_SEVERITY_ROOT:-"$(dirname "${BATS_TEST_FILENAME:-$0}")/.."}
  severity_files=$(mktemp) || return 1
  severity_rows=$(mktemp) || {
    rm -f "$severity_files"
    return 1
  }

  if ! (cd "$severity_root" && git ls-files 'packages/*/*/alerts/*.yaml' > "$severity_files"); then
    rm -f "$severity_files" "$severity_rows"
    return 1
  fi

  if ! : > "$severity_rows"; then
    rm -f "$severity_files" "$severity_rows"
    return 1
  fi
  while IFS= read -r file; do
    if ! (cd "$severity_root" && FILE="$file" yq 'select(.kind == "PrometheusRule" or .kind == "VMRule") | .spec.groups[]? | .rules[]? | select(has("alert")) | [strenv(FILE), .alert, ((.labels.severity // "<missing>") | tostring)] | @tsv' "$file") >> "$severity_rows"; then
      echo "$file: failed to parse alert rules" >&2
      rm -f "$severity_files" "$severity_rows"
      return 1
    fi
  done < "$severity_files"

  if ! sed '/^[[:space:]]*$/d' "$severity_rows"; then
    rm -f "$severity_files" "$severity_rows"
    return 1
  fi
  rm -f "$severity_files" "$severity_rows"
}

@test "alert rules are discovered" {
  rows=$(mktemp) || return 1
  if ! alert_severity_rows > "$rows"; then
    rm -f "$rows"
    return 1
  fi
  count=$(wc -l < "$rows" | tr -d ' ')
  rm -f "$rows"
  [ "$count" -gt 0 ]
}

@test "every alert rule carries a severity the alert sink accepts" {
  rows=$(mktemp) || return 1
  if ! alert_severity_rows > "$rows"; then
    rm -f "$rows"
    return 1
  fi
  bad=0
  tab=$(printf '\t')
  while IFS="$tab" read -r file alert severity; do
    case "$ALERT_SEVERITY_ALLOWED" in
      *" $severity "*) ;;
      *)
        echo "$file: $alert -> '$severity'" >&2
        bad=$((bad + 1))
        ;;
    esac
  done < "$rows"
  rm -f "$rows"
  [ "$bad" -eq 0 ]
}

@test "a parser failure cannot leave the contract green" {
  fixture=$(mktemp -d)
  mkdir -p "$fixture/packages/system/example/alerts"
  printf '%s\n' \
    'apiVersion: operator.victoriametrics.com/v1beta1' \
    'kind: VMRule' \
    'spec:' \
    '  groups:' \
    '  - name: valid' \
    '    rules:' \
    '    - alert: ValidAlert' \
    '      labels:' \
    '        severity: warning' \
    > "$fixture/packages/system/example/alerts/00-valid.yaml"
  printf '%s\n' \
    'apiVersion: operator.victoriametrics.com/v1beta1' \
    'kind: VMRule' \
    'spec: [' \
    > "$fixture/packages/system/example/alerts/99-malformed.yaml"
  git -C "$fixture" init -q
  git -C "$fixture" add packages/system/example/alerts/00-valid.yaml packages/system/example/alerts/99-malformed.yaml

  parser_succeeded=0
  ALERT_SEVERITY_ROOT="$fixture" alert_severity_rows >/dev/null 2>&1 || parser_succeeded=$?
  rm -rf "$fixture"
  [ "$parser_succeeded" -ne 0 ]
}
