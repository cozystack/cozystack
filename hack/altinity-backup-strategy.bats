#!/usr/bin/env bats
# Behavioural tests for the shipped ClickHouse (Altinity) strategy script,
# extracted from the rendered cozy-default-altinity Strategy CR and EXECUTED
# against a stubbed clickhouse-backup sidecar API.
#
# The contract pinned here is the start gate: the sidecar serialises its
# operations and answers any new one with HTTP 423 while another is "in
# progress", so a strategy Pod that starts its operation without waiting for
# an idle sidecar burns a Job attempt in seconds. The gate has to fail the Pod
# rather than proceed when the sidecar never becomes idle.
#
# Run from the repo root with make bats-unit-tests. The rendered script is run
# with `bash`, not `sh`: it opens `set -euo pipefail`, which the production
# image's busybox ash supports but the CI runner's /bin/sh (dash) rejects.

load test_helper

CHART=packages/system/backupstrategy-controller

# Render the shipped strategy's in-Pod script into $1 with the controller-render
# placeholders substituted ($2 mode, $3 application name). An empty render, or
# a placeholder left unsubstituted, is an early failure: these tests EXECUTE it.
render_script() {
  out=$1; mode=$2; app=$3
  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.bucketNameOverride=test-bucket 2>/dev/null \
    | yq eval 'select(.kind == "Altinity") | .spec.template.spec.containers[0].args[0]' - \
    | sed -e "s#{{ .Mode }}#${mode}#g" \
          -e "s#{{ .Release.Name }}#${app}#g" \
          -e "s#{{ .Release.Namespace }}#tenant-x#g" \
          -e "s#{{ .Backup.ApplicationRef.Name }}#${app}#g" > "$out"
  [ -s "$out" ] || return 1
  if grep -q '{{' "$out"; then
    echo "rendered script still holds a controller placeholder" >&2
    return 1
  fi
}

# A curl stub standing in for the sidecar API, put first on PATH next to a
# no-op sleep. State lives in $1:
#   unreachable  present: every request fails as a refused connection (exit 7)
#   busy         a number: that many GET /backup/actions answers still list an
#                operation "in progress"; while it is positive a POST is
#                refused the way the sidecar refuses it (423, curl -f exit 22)
#   posted       the operation the script started, once it started one
#   requests     one line per request: method and URL
make_stubs() {
  bin=$1; state=$2
  mkdir -p "$bin" "$state"
  cat > "$bin/curl" <<STUB
#!/bin/bash
state="$state"
method=GET
url=
prev=
for a in "\$@"; do
  if [ "\$prev" = "-X" ]; then method=\$a; fi
  case "\$a" in http://*) url=\$a ;; esac
  prev=\$a
done
echo "\$method \$url" >> "\$state/requests"
if [ -e "\$state/unreachable" ]; then exit 7; fi
busy=\$(cat "\$state/busy" 2>/dev/null || echo 0)
case "\$method \$url" in
  "POST "*)
    if [ "\$busy" -gt 0 ]; then exit 22; fi
    echo "\${url##*/}" > "\$state/posted"
    ;;
  "GET "*/backup/actions)
    echo '{"command":"create_remote clickhouse-myapp-20260101T000000Z","status":"error","start":"2026-01-01 00:00:00","error":"old"}'
    if [ "\$busy" -gt 0 ]; then
      echo \$((busy - 1)) > "\$state/busy"
      echo '{"command":"upload --resumable=1 clickhouse-myapp-20260101T000000Z","status":"in progress","start":"2026-01-01 00:01:00"}'
    elif [ -s "\$state/posted" ]; then
      echo "{\"command\":\"\$(cat "\$state/posted")\",\"status\":\"success\",\"start\":\"2026-01-01 00:02:00\"}"
    fi
    ;;
  "GET "*/backup/list/remote)
    echo '{"name":"clickhouse-myapp-20260101T000000Z","created":"2026-01-01 00:00:00"}'
    ;;
esac
exit 0
STUB
  cat > "$bin/sleep" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod 0755 "$bin/curl" "$bin/sleep"
}

run_script() {
  tmp=$1
  API_USERNAME=u API_PASSWORD=p PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
}

@test "an empty render fails the fixture instead of passing as a no-op" {
  tmp=$(mktemp -d)
  CHART=$tmp/no-such-chart
  if render_script "$tmp/s.sh" backup myapp 2>/dev/null; then
    echo "FAIL: render_script reported success with no chart"
    false
  fi
}

@test "backup on an idle sidecar starts create_remote and waits for its success" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" backup myapp
  run_script "$tmp"
  grep -q '^POST .*/backup/create_remote?name=clickhouse-myapp-' "$tmp/state/requests"
}

@test "backup waits until the running operation finishes before create_remote" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 3 > "$tmp/state/busy"
  render_script "$tmp/s.sh" backup myapp
  run_script "$tmp"
  grep -q '^POST .*/backup/create_remote?name=clickhouse-myapp-' "$tmp/state/requests"
  # All three busy answers were consumed before the operation was started.
  [ "$(cat "$tmp/state/busy")" = 0 ]
}

@test "backup fails without starting create_remote when the sidecar stays busy" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 1000 > "$tmp/state/busy"
  render_script "$tmp/s.sh" backup myapp
  if run_script "$tmp"; then echo "FAIL: a busy sidecar must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then echo "FAIL: no operation may start on a busy sidecar"; false; fi
}

@test "backup fails without starting create_remote when the sidecar never answers" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  touch "$tmp/state/unreachable"
  render_script "$tmp/s.sh" backup myapp
  if run_script "$tmp"; then echo "FAIL: an unreachable sidecar must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then echo "FAIL: no operation may start on an unreachable sidecar"; false; fi
}

@test "restore fails without starting restore_remote when the sidecar stays busy" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 1000 > "$tmp/state/busy"
  render_script "$tmp/s.sh" restore myapp
  if run_script "$tmp"; then echo "FAIL: a busy sidecar must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then echo "FAIL: no restore may start on a busy sidecar"; false; fi
}
