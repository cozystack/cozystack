#!/usr/bin/env bats
# Behavioural tests for the shipped ClickHouse (Altinity) strategy script,
# extracted from the rendered cozy-default-altinity Strategy CR and EXECUTED
# against a stubbed clickhouse-backup sidecar API.
#
# The contract pinned here is the start gate: the sidecar serialises its
# operations and answers any new one with HTTP 423 while another is "in
# progress", so a strategy Pod that starts its operation without waiting for
# an idle sidecar burns a Job attempt in seconds. The gate has to fail the Pod
# rather than proceed when the sidecar never becomes idle. Once started, an
# operation the sidecar no longer lists (its action log lives in memory and a
# restart empties it) has to fail the Pod too, since the Job has no deadline.
# A Job retry must not start its operation twice: it takes over the
# restore_remote, or the create_remote of the archive the controller named,
# that the attempt before it left running, and a few lost polls do not end an
# attempt in the first place.
#
# Which archive a restore reads back is decided by the script's own jq
# selection over list/remote, so it is exercised against several archives
# rather than string-matched: exactly the archive the Backup recorded, or for
# a Backup that records none, the newest one stamped no later than its takenAt.
#
# Run from the repo root with make bats-unit-tests. The rendered script is run
# with `bash`, not `sh`: it opens `set -euo pipefail`, which the production
# image's busybox ash supports but the CI runner's /bin/sh (dash) rejects.

load test_helper

CHART=packages/system/backupstrategy-controller

# Render the shipped strategy's in-Pod script into $1 and substitute the
# controller-render placeholders the way the driver fills them: $2 mode, $3
# app (release and source application), $4 the archive name the controller
# chose or the Backup recorded (empty, the default, for none), $5 the Backup
# name, $6 its takenAt in the archive-name layout (default: later than every
# archive the stub lists). An empty render fails early, as does a placeholder
# left unsubstituted: these tests EXECUTE it.
render_script() {
  out=$1; mode=$2; app=$3; archive=${4-}; backup=${5-bkp}; takenat=${6-99991231T235959Z}
  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.bucketNameOverride=test-bucket 2>/dev/null \
    | yq eval 'select(.kind == "Altinity") | .spec.template.spec.containers[0].args[0]' - \
    | sed -e "s#{{ .Mode }}#${mode}#g" \
          -e "s#{{ .Release.Name }}#${app}#g" \
          -e "s#{{ .Release.Namespace }}#tenant-x#g" \
          -e "s#{{ .Backup.ApplicationRef.Name }}#${app}#g" \
          -e "s#{{ .Backup.Name }}#${backup}#g" \
          -e "s#{{ .ArchiveName | default \"\" }}#${archive}#g" \
          -e "s#{{ .Backup.TakenAt | default \"\" }}#${takenat}#g" > "$out"
  [ -s "$out" ] || return 1
  if grep -q '{{' "$out"; then
    echo "rendered script still holds a controller placeholder" >&2
    return 1
  fi
}

# A curl stub standing in for the sidecar API, put first on PATH next to a
# no-op sleep. State lives in $1:
#   unreachable     present: every request fails as a refused connection (7)
#   down            a number: that many next requests fail the same way, as
#                   before the sidecar has started
#   busy            a number: that many GET /backup/actions answers still list
#                   an operation of someone else "in progress"
#   op              the command line of the sidecar's own operation, written
#                   by an accepted POST the way the sidecar logs it ("--resume"
#                   before the name of a create_remote asking for it), or
#                   seeded by a test as the operation
#                   an earlier attempt of the same Job started
#   op_left         how many more GET /backup/actions answers list op as
#                   "in progress" before it ends with op_result
#   op_duration     op_left given to an operation a POST starts (default 0)
#   op_result       the status op ends with (default success)
#   prior           extra action-log rows, verbatim, older than op
#   remote          present: the list/remote answer, verbatim, in place of the
#                   one archive listed by default
#   restarted       present: the action log no longer lists op, as after the
#                   sidecar restarted and lost its in-memory log
#   fail_on_post    a POST that is accepted makes that many following
#                   GET /backup/actions requests fail (curl exit 7)
#   idle            created by an answer that lists nothing in progress; a
#                   POST is refused (423, curl -f exit 22) unless one was seen
#                   since the last accepted POST, and while anything runs
#   accepted        one line per POST the sidecar accepted
#   requests        one line per request: method and URL
#   runaway         created when the script exceeds 200 requests; that request
#                   and every later one fails, so a polling loop that never ends
#                   stops the test instead of hanging it
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
if [ "\$(wc -l < "\$state/requests")" -gt 200 ]; then touch "\$state/runaway"; exit 7; fi
if [ -e "\$state/unreachable" ]; then exit 7; fi
num() { cat "\$state/\$1" 2>/dev/null || echo 0; }
down=\$(num down)
if [ "\$down" -gt 0 ]; then echo \$((down - 1)) > "\$state/down"; exit 7; fi
running=0
if [ -s "\$state/op" ] && [ ! -e "\$state/restarted" ] && [ "\$(num op_left)" -gt 0 ]; then running=1; fi
case "\$method \$url" in
  "POST "*)
    if [ "\$(num busy)" -gt 0 ] || [ "\$running" = 1 ] || [ ! -e "\$state/idle" ]; then exit 22; fi
    case "\$url" in
      */create_remote\?name=*)
        name=\${url#*name=}; name=\${name%%&*}
        case "\$url" in *[?\&]resume=true*) cmd="create_remote --resume \$name" ;; *) cmd="create_remote \$name" ;; esac
        ;;
      */restore_remote/*) cmd="restore_remote \${url##*/}" ;;
      *) exit 22 ;;
    esac
    echo "\$cmd" > "\$state/op"
    echo "\$cmd" >> "\$state/accepted"
    num op_duration > "\$state/op_left"
    num fail_on_post > "\$state/fail_polls"
    rm -f "\$state/idle"
    ;;
  "GET "*/backup/actions)
    fail=\$(num fail_polls)
    if [ "\$fail" -gt 0 ]; then echo \$((fail - 1)) > "\$state/fail_polls"; exit 7; fi
    echo '{"command":"create_remote clickhouse-myapp-20260101T000000Z","status":"error","start":"2026-01-01 00:00:00","error":"old"}'
    cat "\$state/prior" 2>/dev/null || true
    inprog=0
    busy=\$(num busy)
    if [ "\$busy" -gt 0 ]; then
      echo \$((busy - 1)) > "\$state/busy"
      echo '{"command":"upload --resumable=1 clickhouse-myapp-20260101T000000Z","status":"in progress","start":"2026-01-01 00:01:00"}'
      inprog=1
    fi
    if [ -s "\$state/op" ] && [ ! -e "\$state/restarted" ]; then
      left=\$(num op_left)
      if [ "\$left" -gt 0 ]; then
        echo \$((left - 1)) > "\$state/op_left"
        status="in progress"
        inprog=1
      else
        status=\$(cat "\$state/op_result" 2>/dev/null || echo success)
      fi
      echo "{\"command\":\"\$(cat "\$state/op")\",\"status\":\"\$status\",\"start\":\"2026-01-01 00:02:00\",\"error\":\"boom\"}"
    fi
    if [ "\$inprog" = 0 ]; then touch "\$state/idle"; fi
    ;;
  "GET "*/backup/list/remote)
    if [ -e "\$state/remote" ]; then cat "\$state/remote"; else
      echo '{"name":"clickhouse-myapp-20260101T000000Z","created":"2026-01-01 00:00:00"}'
    fi
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

# One list/remote row per argument, "name" or "name|desc"; created mirrors
# the name's timestamp.
remote_rows() {
  for row in "$@"; do
    name=${row%%|*}; desc="tar, regular"
    if [ "$row" != "$name" ]; then desc=${row#*|}; fi
    ts=${name##*-}
    printf '{"name":"%s","created":"%s","location":"remote","required":"","desc":"%s"}\n' "$name" "$ts" "$desc"
  done
}

accepted() {
  if [ -e "$1/state/accepted" ]; then wc -l < "$1/state/accepted" | tr -d ' '; else echo 0; fi
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
  grep -q '^create_remote --resume clickhouse-myapp-' "$tmp/state/accepted"
  [ "$(cat "$tmp/state/busy")" = 0 ]
}

@test "restore on an idle sidecar starts restore_remote of the listed archive" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" restore myapp
  run_script "$tmp"
  grep -q '^POST .*/backup/restore_remote/clickhouse-myapp-20260101T000000Z$' "$tmp/state/requests"
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

@test "restore fails without starting restore_remote when the sidecar never answers" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  touch "$tmp/state/unreachable"
  render_script "$tmp/s.sh" restore myapp
  if run_script "$tmp" 2> "$tmp/err"; then echo "FAIL: an unreachable sidecar must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then echo "FAIL: no restore may start on an unreachable sidecar"; false; fi
  grep -q 'list/remote failed 60 times in a row' "$tmp/err"
  [ "$(grep -c '^GET .*/backup/list/remote$' "$tmp/state/requests")" = 60 ]
}

@test "restore fails without starting restore_remote when the sidecar stays busy" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 1000 > "$tmp/state/busy"
  render_script "$tmp/s.sh" restore myapp
  if run_script "$tmp"; then echo "FAIL: a busy sidecar must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then echo "FAIL: no restore may start on a busy sidecar"; false; fi
}

@test "backup fails instead of polling forever when the sidecar restarts after create_remote" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  touch "$tmp/state/restarted"
  render_script "$tmp/s.sh" backup myapp
  if run_script "$tmp" 2> "$tmp/err"; then echo "FAIL: an operation lost from the action log must fail the Pod"; false; fi
  grep -q 'no longer in the sidecar action log' "$tmp/err"
  grep -q '^POST .*/backup/create_remote?name=clickhouse-myapp-' "$tmp/state/requests"
  if [ -e "$tmp/state/runaway" ]; then echo "FAIL: the script kept polling for an operation the sidecar no longer lists"; false; fi
}

@test "restore waits for a sidecar that has not started yet" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 10 > "$tmp/state/down"
  render_script "$tmp/s.sh" restore myapp
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
}

@test "restore waits until another operation finishes, then starts its own" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 3 > "$tmp/state/busy"
  render_script "$tmp/s.sh" restore myapp
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
  grep -qx 'restore_remote clickhouse-myapp-20260101T000000Z' "$tmp/state/accepted"
}

@test "failed polls after restore_remote, short of six in a row, do not fail the attempt" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 3 > "$tmp/state/op_duration"
  echo 5 > "$tmp/state/fail_on_post"
  render_script "$tmp/s.sh" restore myapp
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
}

@test "a retry after an attempt lost its polls joins the running restore instead of starting another" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 20 > "$tmp/state/op_duration"
  echo 6 > "$tmp/state/fail_on_post"
  render_script "$tmp/s.sh" restore myapp
  if run_script "$tmp"; then echo "FAIL: six failed polls in a row must fail the attempt"; false; fi
  [ "$(accepted "$tmp")" = 1 ]
  echo 0 > "$tmp/state/fail_polls"
  run_script "$tmp"
  if [ "$(accepted "$tmp")" != 1 ]; then echo "FAIL: the retry started restore_remote a second time"; false; fi
  [ "$(cat "$tmp/state/op_left")" = 0 ]
}

@test "restore does not join an operation running on another archive" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 'restore_remote clickhouse-myapp-20251231T000000Z' > "$tmp/state/op"
  echo 3 > "$tmp/state/op_left"
  render_script "$tmp/s.sh" restore myapp
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
  grep -qx 'restore_remote clickhouse-myapp-20260101T000000Z' "$tmp/state/accepted"
}

# The sidecar never trims its action log, so a finished restore_remote row of
# the same archive may belong to an earlier RestoreJob; only a running one may
# be taken over.
@test "restore starts restore_remote again when the log holds a finished one of the same archive" {
  for status in success error cancel; do
    tmp=$(mktemp -d)
    make_stubs "$tmp/bin" "$tmp/state"
    echo "{\"command\":\"restore_remote clickhouse-myapp-20260101T000000Z\",\"status\":\"$status\",\"start\":\"2026-01-01 00:01:30\"}" > "$tmp/state/prior"
    render_script "$tmp/s.sh" restore myapp
    run_script "$tmp"
    if [ "$(accepted "$tmp")" != 1 ]; then echo "FAIL: a $status row of an earlier restore must not stand in for this one"; false; fi
  done
}

@test "a restore that ends in error fails the Pod" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo error > "$tmp/state/op_result"
  render_script "$tmp/s.sh" restore myapp
  if run_script "$tmp"; then echo "FAIL: a failed restore must fail the Pod"; false; fi
  [ "$(accepted "$tmp")" = 1 ]
}

@test "a cancelled backup fails the Pod instead of polling forever" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo cancel > "$tmp/state/op_result"
  render_script "$tmp/s.sh" backup myapp
  if run_script "$tmp"; then echo "FAIL: a cancelled operation must fail the Pod"; false; fi
  if [ -e "$tmp/state/runaway" ]; then echo "FAIL: the script kept polling a cancelled operation"; false; fi
}

@test "restore reads back the archive the Backup recorded, not the newest one" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" restore myapp clickhouse-myapp-20260304T050607Z bkp-a 20260304T050622Z
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T050607Z \
    clickhouse-myapp-20260304T060000Z clickhouse-myapp-20260304T070000Z > "$tmp/state/remote"
  run_script "$tmp"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-myapp-20260304T050607Z' "$tmp/state/requests"
  [ "$(accepted "$tmp")" = 1 ]
}

@test "restore fails naming the recorded archive when remote storage lacks it" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" restore myapp clickhouse-myapp-20260304T050607Z bkp-a 20260304T050622Z
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T060000Z > "$tmp/state/remote"
  out=""
  if out=$(run_script "$tmp" 2>&1); then
    echo "FAIL: restore succeeded without the recorded archive: $out"
    false
  fi
  printf '%s\n' "$out" | grep -qF 'archive clickhouse-myapp-20260304T050607Z recorded on Backup bkp-a is not in remote storage'
  if grep -qF '/backup/restore_remote/' "$tmp/state/requests"; then
    echo "FAIL: restored another archive: $(cat "$tmp/state/requests")"
    false
  fi
}

@test "restore of a Backup without a recorded archive takes the newest one stamped by its takenAt, and warns" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" restore myapp "" bkp-a 20260304T050800Z
  # Each decoy sorts after the expected archive and is ruled out by one
  # filter: broken, not a bare timestamp after the prefix, or stamped after
  # takenAt.
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T050607Z \
    "clickhouse-myapp-20260304T050700Z|broken (can't stat metadata.json)" \
    clickhouse-myapp-20260304T050710Z-copy \
    myapp-20260304050720-0f3c9a1e5b7d \
    clickhouse-myapp-20260304T060000Z > "$tmp/state/remote"
  out=$(run_script "$tmp" 2>&1)
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-myapp-20260304T050607Z' "$tmp/state/requests"
  [ "$(accepted "$tmp")" = 1 ]
  printf '%s\n' "$out" | grep -qF 'WARNING: Backup bkp-a records no archive name'
}

@test "restore of a Backup without a recorded archive fails when no archive is stamped by its takenAt" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" restore myapp "" bkp-a 20260304T050800Z
  remote_rows clickhouse-myapp-20260304T060000Z clickhouse-myapp-20260304T070000Z > "$tmp/state/remote"
  out=""
  if out=$(run_script "$tmp" 2>&1); then
    echo "FAIL: restore succeeded with every archive newer than takenAt: $out"
    false
  fi
  printf '%s\n' "$out" | grep -qF 'no archive of myapp stamped no later than 20260304T050800Z'
  if grep -qF '/backup/restore_remote/' "$tmp/state/requests"; then
    echo "FAIL: restored an archive newer than takenAt: $(cat "$tmp/state/requests")"
    false
  fi
}

@test "backup writes the archive under the name the controller chose" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/create_remote?name=clickhouse-myapp-20260304T050607Z&resume=true' "$tmp/state/requests"
}

@test "backup falls back to a timestamped name when the controller supplies none" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  render_script "$tmp/s.sh" backup myapp ""
  run_script "$tmp"
  grep -qE '^POST http://[^ ]*/backup/create_remote\?name=clickhouse-myapp-[0-9]{8}T[0-9]{6}Z&resume=true$' "$tmp/state/requests"
}

# The sidecar answers a second operation with 423 while one runs, so a retry
# that POSTed over its predecessor's upload would burn every attempt left.
@test "a retried backup takes over the upload of its archive the sidecar is still running" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 'create_remote --resume clickhouse-myapp-20260304T050607Z' > "$tmp/state/op"
  echo 3 > "$tmp/state/op_left"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  if grep -q '^POST ' "$tmp/state/requests"; then
    echo "FAIL: started another operation over the running one: $(cat "$tmp/state/requests")"
    false
  fi
  [ "$(cat "$tmp/state/op_left")" = 0 ]
}

@test "a taken-over backup that ends in error fails the Pod" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 'create_remote --resume clickhouse-myapp-20260304T050607Z' > "$tmp/state/op"
  echo 2 > "$tmp/state/op_left"
  echo error > "$tmp/state/op_result"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  if run_script "$tmp"; then echo "FAIL: a taken-over upload that failed must fail the Pod"; false; fi
  if grep -q '^POST ' "$tmp/state/requests"; then
    echo "FAIL: started another operation over the running one: $(cat "$tmp/state/requests")"
    false
  fi
}

@test "a retried backup after a failed attempt resumes the same archive" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo '{"command":"create_remote clickhouse-myapp-20260304T050607Z","status":"error","start":"2026-01-01 00:01:30","error":"S3 PutObject 403"}' > "$tmp/state/prior"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/create_remote?name=clickhouse-myapp-20260304T050607Z&resume=true' "$tmp/state/requests"
}

@test "backup does not take over an upload running for another archive" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo 'create_remote --resume clickhouse-myapp-20260304T040000Z' > "$tmp/state/op"
  echo 3 > "$tmp/state/op_left"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
  grep -qx 'create_remote --resume clickhouse-myapp-20260304T050607Z' "$tmp/state/accepted"
}

# The archive is named per BackupJob, so a success row of exactly this
# upload is an earlier attempt of the same BackupJob that finished while it
# could not see it. Uploading again with resume would re-upload over the
# finished archive and break it if that second upload dies halfway.
@test "a retried backup takes the upload of its archive an earlier attempt finished as its result" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo '{"command":"create_remote --resume clickhouse-myapp-20260304T050607Z","status":"success","start":"2026-01-01 00:01:30"}' > "$tmp/state/prior"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  if grep -q '^POST ' "$tmp/state/requests"; then
    echo "FAIL: uploaded again over a finished archive: $(cat "$tmp/state/requests")"
    false
  fi
}

@test "a retried backup resumes an upload of its archive that ended in error or was cancelled" {
  for status in error cancel; do
    tmp=$(mktemp -d)
    make_stubs "$tmp/bin" "$tmp/state"
    echo "{\"command\":\"create_remote --resume clickhouse-myapp-20260304T050607Z\",\"status\":\"$status\",\"start\":\"2026-01-01 00:01:30\"}" > "$tmp/state/prior"
    render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
    run_script "$tmp"
    if [ "$(accepted "$tmp")" != 1 ]; then echo "FAIL: a $status row must lead to a resumed upload"; false; fi
  done
}

@test "a retried backup resumes when its latest upload failed after an earlier one succeeded" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  printf '%s\n' \
    '{"command":"create_remote --resume clickhouse-myapp-20260304T050607Z","status":"success","start":"2026-01-01 00:01:00"}' \
    '{"command":"create_remote --resume clickhouse-myapp-20260304T050607Z","status":"error","start":"2026-01-01 00:01:30"}' > "$tmp/state/prior"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
}

@test "backup does not take another archive's finished upload as its own" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/state"
  echo '{"command":"create_remote --resume clickhouse-myapp-20260304T040000Z","status":"success","start":"2026-01-01 00:01:30"}' > "$tmp/state/prior"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z
  run_script "$tmp"
  [ "$(accepted "$tmp")" = 1 ]
}
