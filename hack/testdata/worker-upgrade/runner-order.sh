#!/usr/bin/env bash
set -euo pipefail
root=$PWD
work=$(mktemp -d)
mkdir -p "$work/hack/e2e-chainsaw/_lib" "$work/bin" "$work/_out/cozyreport/snapshots/worker-upgrade"
printf '%s\n' '[{"name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"worker-root"},{"name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-cluster-api","uid":"kairos-root"}]' > "$work/_out/cozyreport/snapshots/worker-upgrade/platform-roots.json"
printf '%s\n' '[]' > "$work/_out/worker-upgrade-platform-before.json"
cp hack/e2e-chainsaw-run.sh "$work/hack/e2e-chainsaw-run.sh"
cp hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh "$work/hack/e2e-chainsaw/_lib/run-kubernetes-upgrade.sh"
cat > "$work/hack/e2e-chainsaw/_lib/leak-guard.sh" <<'SH'
LG_BUDGET=${COZY_LEAK_BUDGET:-300}
lg_allowlist_validate() { :; }
lg_apiserver_now() {
  case "${FAIL_UPGRADE:-}" in
    clock-hang) echo read-started >> "$EVENTS"; sleep "${FIXTURE_SLEEP:-6}" ;;
    clock-late) : > "$ADVANCE_CLOCK"; echo read-past-deadline >> "$EVENTS" ;;
  esac
  if [ "${FAIL_UPGRADE:-}" = clock-fail ] && [ ! -f "$READ_MARKER" ]; then
    : > "$READ_MARKER"
    return 1
  fi
  echo 1
}
lg_baseline() {
  case "${FAIL_UPGRADE:-}" in
    baseline-hang) echo read-started >> "$EVENTS"; sleep "${FIXTURE_SLEEP:-6}" ;;
    baseline-late) : > "$ADVANCE_CLOCK"; echo read-past-deadline >> "$EVENTS" ;;
  esac
  if [ "${FAIL_UPGRADE:-}" = baseline-fail ] && [ ! -f "$READ_MARKER" ]; then
    : > "$READ_MARKER"
    return 1
  fi
  echo baseline
}
lg_check_suite() {
  printf '%s\n' "$1" >> "$LG_CHECKED"
  case "${FAIL_UPGRADE:-}" in
    post-hang) [ "$1" != kubernetes-latest ] || sleep "${FIXTURE_SLEEP:-6}" ;;
    post-read-error) return 2 ;;
    post-proven-leak) return 1 ;;
    post-state)
      [ "$2" = 1 ] && [ "$3" = baseline ] && [ "$4" = 0 ] || return 2
      if [ "$1" = kubernetes-latest ]; then
        [ "$LG_BASELINE_ROOT_UIDS" = '["kairos-root","worker-root"]' ] || return 2
      else
        [ "$LG_BASELINE_ROOT_UIDS" = '[]' ] || return 2
      fi
      ;;
  esac
}
lg_merge_reports() {
  local f dir=$1 suite; shift
  for f in "$dir"/*.unchecked; do
    [ -e "$f" ] || continue
    if [ -s "$f" ]; then sed 's/^/unchecked: /' "$f" >> "$UNCHECKED_OUT"
    else printf '%s\n' 'unchecked: the leak guard could not read the cluster' >> "$UNCHECKED_OUT"; fi
  done
  for f in "$dir"/*.leaked; do [ -e "$f" ] && printf 'leaked: %s\n' "${f##*/}" >> "$LEAKED_OUT"; done
  for suite in "$@"; do [ -f "$dir/${suite//\//_}.xml" ] || printf 'no report: %s\n' "$suite" >> "$NO_REPORT"; done
  return 0
}
lg_synthetic_suite() { printf '%s\n' "$3" >> "$SYNTHETIC"; printf '%s: %s\n' "$1" "$3" >> "$SYNTHETIC_SUITES"; }
SH
cat > "$work/bin/date" <<'SH'
#!/usr/bin/env bash
if [ -f "${ADVANCE_CLOCK:-/nonexistent}" ]; then
  value=$(cat "$ADVANCE_CLOCK")
  case "$value" in
    remaining:*) printf '%s\n' "$((COZY_WORKER_UPGRADE_DEADLINE - ${value#remaining:}))" ;;
    *) printf '%s\n' "$COZY_WORKER_UPGRADE_DEADLINE" ;;
  esac
elif [ "$*" = +%s ]; then
  # Admission checks see one instant, so load cannot refuse a phase the test
  # expects to start; every bound still runs on real time.
  printf '%s\n' "$FROZEN_NOW"
else
  /bin/date "$@"
fi
SH
chmod +x "$work/bin/date"
export ADVANCE_CLOCK="$work/advance-clock" FROZEN_NOW="$(/bin/date +%s)"
cat > "$work/bin/chainsaw" <<'SH'
#!/usr/bin/env bash
suite=${!#}
echo "$suite" >> "$EVENTS"
if [ "$suite" = kubernetes-latest ]; then
  case "${FAIL_UPGRADE:-}" in
    suite-raw-background)
      sh -c 'trap "" TERM; sleep "${FIXTURE_SLEEP:-6}"; printf "%s\n" nested-complete >> "$LATE_MARKER"' &
      ;;
    suite-background)
      timeout 20 sh -c 'trap "" TERM; sleep "${FIXTURE_SLEEP:-6}"; printf "%s\n" nested-complete >> "$LATE_MARKER"' &
      ;;
    suite-nested) timeout 20 sh -c 'trap "" TERM; sleep "${FIXTURE_SLEEP:-6}"; printf "%s\n" nested-complete >> "$LATE_MARKER"' ;;
    suite-cozy-nested)
      . _lib/run-kubernetes-upgrade.sh
      cozy_worker_upgrade_timeout 20s sh -c 'trap "" TERM; sleep "${FIXTURE_SLEEP:-6}"; printf "%s\n" nested-complete >> "$LATE_MARKER"'
      ;;
  esac
fi
[ "${FAIL_UPGRADE:-}" != post-budget-gone ] || : > "$ADVANCE_CLOCK"
if [ -n "${RUNNER_POST_REMAINING:-}" ]; then printf 'remaining:%s\n' "$RUNNER_POST_REMAINING" > "$ADVANCE_CLOCK"; fi
case "${FAIL_UPGRADE:-}" in fail|fail-noroots|fail-nobefore|fail-notrun) [ "$suite" != kubernetes-latest ] ;; esac
SH
cat > "$work/hack/cozytest.sh" <<'SH'
echo openapi >> "$EVENTS"
if [ "${FAIL_UPGRADE:-}" = openapi-nested ]; then
  timeout 20 sh -c 'trap "" TERM; sleep "${FIXTURE_SLEEP:-6}"; printf "%s\n" nested-complete >> "$LATE_MARKER"'
fi
[ "${FAIL_UPGRADE:-}" != openapi-hang ] || exec sleep 60
if [ "${FAIL_UPGRADE:-}" = openapi-stubborn ]; then trap '' TERM; sleep 60; fi
SH
chmod +x "$work/bin/chainsaw"
export LATE_MARKER="$work/late-marker" LEAKED_OUT="$work/leaked" UNCHECKED_OUT="$work/unchecked" LG_CHECKED="$work/lg-checked" PATH="$work/bin:$PATH" EVENTS="$work/events" SYNTHETIC="$work/synthetic" SYNTHETIC_SUITES="$work/synthetic-suites" NO_REPORT="$work/no-report" FAIL_UPGRADE="${1:-}" COZY_WORKER_UPGRADE_MODE=required
export COZY_LEAK_BUDGET=${RUNNER_LEAK_BUDGET:-300}
export COZY_WORKER_UPGRADE_DEADLINE=$(( $(date +%s) + ${RUNNER_HEADROOM:-9600} )) READ_MARKER="$work/read-failed"
: > "$EVENTS"
case "$FAIL_UPGRADE" in *-noroots) rm "$work/_out/cozyreport/snapshots/worker-upgrade/platform-roots.json" ;; esac
case "$FAIL_UPGRADE" in *-nobefore) rm "$work/_out/cozyreport/snapshots/worker-upgrade/platform-roots.json" "$work/_out/worker-upgrade-platform-before.json" ;; esac
if [ "$FAIL_UPGRADE" = pass-emptyroots ]; then
  printf '%s\n' '[]' > "$work/_out/cozyreport/snapshots/worker-upgrade/platform-roots.json"
  printf '%s\n' '[{"kind":"Deployment","name":"kubernetes-worker-classes","namespace":"cozy-system","uid":"worker-root"},{"kind":"Deployment","name":"capi-kairos-bootstrap-controller-manager","namespace":"cozy-cluster-api","uid":"kairos-root"}]' > "$work/_out/worker-upgrade-platform-before.json"
fi
rc=0
case "$FAIL_UPGRADE" in suite-raw-background|suite-background|suite-nested|suite-cozy-nested|openapi-nested)
  captured=$(cd "$work/hack/e2e-chainsaw" && bash ../e2e-chainsaw-run.sh postgres kubernetes-latest bucket 2>&1) || rc=$?
  printf '%s\n' "$captured" > "$work/output"
  ;;
  *) (cd "$work/hack/e2e-chainsaw" && bash ../e2e-chainsaw-run.sh postgres kubernetes-latest bucket) > "$work/output" 2>&1 || rc=$? ;;
esac
cat "$EVENTS"
[ ! -e "$LATE_MARKER" ] || cat "$LATE_MARKER"
case "$FAIL_UPGRADE" in suite-nested|suite-cozy-nested|clock-hang|baseline-hang|clock-late|baseline-late|openapi-*|*-noroots|*-nobefore|*-emptyroots) [ ! -e "$work/synthetic" ] || sed 's/^/synthetic: /' "$work/synthetic" ;; esac
if [ "$FAIL_UPGRADE" = fail-notrun ]; then
  [ ! -e "$work/synthetic-suites" ] || cat "$work/synthetic-suites"
  [ ! -e "$work/no-report" ] || cat "$work/no-report"
fi
case "$FAIL_UPGRADE" in fail-noroots|fail-nobefore)
  if grep -qx kubernetes-latest "$work/lg-checked" 2>/dev/null; then echo "leak guard ran on kubernetes-latest"; fi
  [ ! -e "$work/unchecked" ] || cat "$work/unchecked"
  ;;
esac
if [ "$FAIL_UPGRADE" = post-budget-gone ] && [ -e "$LG_CHECKED" ]; then echo post-check-was-invoked; fi
case "$FAIL_UPGRADE" in post-*)
  [ ! -e "$work/unchecked" ] || cat "$work/unchecked"
  [ ! -e "$work/leaked" ] || cat "$work/leaked"
  ;;
esac
cd "$root"
exit "$rc"
