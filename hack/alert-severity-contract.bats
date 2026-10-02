#!/usr/bin/env bats

# Alerta's default alarm model, the set the webhook accepts. `none` is left out
# on purpose: the Alertmanager config routes severity="none" to a blackhole
# receiver, so such an alert is never delivered, which is the outcome this
# contract exists to catch. Rule files under packages/**/charts/** are out of
# scope: they are re-vendored by `make update`, so a bad severity there is
# fixed upstream or in patches/, not by editing the label.
ALERT_SEVERITY_ALLOWED=" security critical major minor warning indeterminate informational normal ok cleared debug trace unknown "

helm_actions_stripped() {
  awk '
    function strip_comments(s,    pre, rest) {
      while (1) {
        if (incomment) {
          if (match(s, /\*\/[ \t]*-?[}][}]/)) { s = substr(s, RSTART + RLENGTH); incomment = 0 } else { return "" }
        }
        if (match(s, /[{][{]-?[ \t]*\/\*/)) {
          pre = substr(s, 1, RSTART - 1); rest = substr(s, RSTART + RLENGTH)
          if (match(rest, /\*\/[ \t]*-?[}][}]/)) { s = pre substr(rest, RSTART + RLENGTH) } else { incomment = 1; return pre }
        } else { return s }
      }
    }
    {
      line = strip_comments($0)
      if (incomment && line ~ /^[ \t]*$/) { next }
      if (line ~ /^[ \t]*[{][{].*[}][}][ \t]*$/) { next }
      gsub(/[{][{][^}]*[}][}]/, "TEMPLATED", line)
      print line
    }
  ' "$1"
}

alert_severity_rows() {
  severity_root=${ALERT_SEVERITY_ROOT:-"$(dirname "${BATS_TEST_FILENAME:-$0}")/.."}
  severity_files=$(mktemp) || return 1
  severity_rows=$(mktemp) || {
    rm -f "$severity_files"
    return 1
  }
  severity_stripped=$(mktemp) || {
    rm -f "$severity_files" "$severity_rows"
    return 1
  }

  if ! (
    cd "$severity_root" &&
      git grep -Il -E '^[[:space:]]*kind:[[:space:]]*(PrometheusRule|VMRule)[[:space:]]*$' -- \
        'packages/**/*.yaml' \
        'packages/**/*.yml' \
        ':(exclude)packages/**/charts/**'
  ) > "$severity_files"; then
    rm -f "$severity_files" "$severity_rows" "$severity_stripped"
    return 1
  fi

  if ! : > "$severity_rows"; then
    rm -f "$severity_files" "$severity_rows" "$severity_stripped"
    return 1
  fi
  while IFS= read -r file; do
    if ! (cd "$severity_root" && helm_actions_stripped "$file" > "$severity_stripped" && FILE="$file" yq 'select(.kind == "PrometheusRule" or .kind == "VMRule") | .spec.groups[]? | .rules[]? | select(has("alert")) | [strenv(FILE), .alert, ((.labels.severity // "<missing>") | tostring)] | @tsv' "$severity_stripped") >> "$severity_rows"; then
      echo "$file: failed to parse alert rules" >&2
      rm -f "$severity_files" "$severity_rows" "$severity_stripped"
      return 1
    fi
  done < "$severity_files"

  if ! sed '/^[[:space:]]*$/d' "$severity_rows"; then
    rm -f "$severity_files" "$severity_rows" "$severity_stripped"
    return 1
  fi
  rm -f "$severity_files" "$severity_rows" "$severity_stripped"
}

alert_severity_contract() {
  rows=$(mktemp) || return 1
  if ! alert_severity_rows > "$rows"; then
    rm -f "$rows"
    return 1
  fi
  bad=0
  tab=$(printf '\t')
  while IFS="$tab" read -r file alert severity; do
    accepted=0
    for allowed in $ALERT_SEVERITY_ALLOWED; do
      if [ "$allowed" = "$severity" ]; then
        accepted=1
        break
      fi
    done
    if [ "$accepted" -eq 0 ]; then
      echo "$file: $alert -> '$severity'" >&2
      bad=$((bad + 1))
    fi
  done < "$rows"
  rm -f "$rows"
  [ "$bad" -eq 0 ] || return 1
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
  alert_severity_contract
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

@test "template rule sources cannot bypass the severity contract" {
  fixture=$(mktemp -d)
  mkdir -p "$fixture/packages/extra/example/templates"
  printf '%s\n' \
    'apiVersion: monitoring.coreos.com/v1' \
    'kind: PrometheusRule' \
    'spec:' \
    '  groups:' \
    '  - name: template' \
    '    rules:' \
    '    - alert: InvalidTemplateSeverity' \
    '      labels:' \
    '        severity: warn' \
    > "$fixture/packages/extra/example/templates/prometheus-rules.yaml"
  git -C "$fixture" init -q
  git -C "$fixture" add packages/extra/example/templates/prometheus-rules.yaml

  contract_succeeded=0
  ALERT_SEVERITY_ROOT="$fixture" alert_severity_contract >/dev/null 2>&1 || contract_succeeded=$?
  rm -rf "$fixture"
  [ "$contract_succeeded" -ne 0 ]
}

@test "helm template actions do not hide a rule from the contract" {
  fixture=$(mktemp -d)
  mkdir -p "$fixture/packages/system/example/templates"
  printf '%s\n' \
    '{{- if .Values.alerts.enabled }}' \
    '{{- /*' \
    'Rules gated on a value, behind a comment that spans lines.' \
    '*/}}' \
    'apiVersion: monitoring.coreos.com/v1' \
    'kind: PrometheusRule' \
    'metadata:' \
    '  name: example' \
    '  namespace: {{ .Release.Namespace }}' \
    'spec:' \
    '  groups:' \
    '  - name: example' \
    '    rules:' \
    '    {{- /* one-line comment */}}' \
    '    - alert: TemplatedRuleWithBadSeverity' \
    '      expr: up == 0' \
    '      labels:' \
    '        severity: warn' \
    '      annotations:' \
    '        description: {{ "{{ $labels.instance }}" }} is down' \
    '{{- end }}' \
    > "$fixture/packages/system/example/templates/alerts.yaml"
  git -C "$fixture" init -q
  git -C "$fixture" add packages/system/example/templates/alerts.yaml

  rows=$(mktemp)
  parser_succeeded=0
  ALERT_SEVERITY_ROOT="$fixture" alert_severity_rows > "$rows" 2>/dev/null || parser_succeeded=$?
  found=$(grep -c 'TemplatedRuleWithBadSeverity' "$rows" || true)
  contract_succeeded=0
  ALERT_SEVERITY_ROOT="$fixture" alert_severity_contract >/dev/null 2>&1 || contract_succeeded=$?
  rm -rf "$fixture" "$rows"
  [ "$parser_succeeded" -eq 0 ]
  [ "$found" -eq 1 ]
  [ "$contract_succeeded" -ne 0 ]
}

@test "two accepted words in one severity are rejected" {
  fixture=$(mktemp -d)
  mkdir -p "$fixture/packages/system/example/alerts"
  printf '%s\n' \
    'apiVersion: operator.victoriametrics.com/v1beta1' \
    'kind: VMRule' \
    'spec:' \
    '  groups:' \
    '  - name: joined' \
    '    rules:' \
    '    - alert: JoinedSeverity' \
    '      labels:' \
    '        severity: critical major' \
    > "$fixture/packages/system/example/alerts/00-joined.yaml"
  git -C "$fixture" init -q
  git -C "$fixture" add packages/system/example/alerts/00-joined.yaml

  contract_succeeded=0
  ALERT_SEVERITY_ROOT="$fixture" alert_severity_contract >/dev/null 2>&1 || contract_succeeded=$?
  rm -rf "$fixture"
  [ "$contract_succeeded" -ne 0 ]
}
