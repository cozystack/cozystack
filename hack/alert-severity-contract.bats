#!/usr/bin/env bats

ALERT_SEVERITY_ALLOWED=" security critical major minor warning indeterminate informational normal ok cleared debug trace unknown "

alert_severity_rows() {
  cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." || return 1
  git ls-files 'packages/*/*/alerts/*.yaml' | while IFS= read -r file; do
    yq "select(.kind == \"PrometheusRule\" or .kind == \"VMRule\") | .spec.groups[]? | .rules[]? | select(has(\"alert\")) | [\"$file\", .alert, ((.labels.severity // \"<missing>\") | tostring)] | @tsv" "$file"
  done | grep -v '^[[:space:]]*$'
}

@test "alert rules are discovered" {
  count=$(alert_severity_rows | wc -l | tr -d ' ')
  [ "$count" -gt 0 ]
}

@test "every alert rule carries a severity the alert sink accepts" {
  rows=$(mktemp)
  alert_severity_rows > "$rows"
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
