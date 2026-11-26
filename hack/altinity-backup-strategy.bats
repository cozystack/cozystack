#!/usr/bin/env bats
# Behavioural tests for the shipped Altinity (ClickHouse) backup/restore
# strategy script, extracted from the rendered cozy-default-altinity Strategy
# CR and EXECUTED against a stubbed clickhouse-backup HTTP API, with the real
# jq doing the archive selection. Which archive a restore reads back is
# decided by that selection over list/remote, so it is exercised against
# several archives rather than string-matched.
#
# The script runs with `bash`, not `sh`, for the reason given in
# hack/redis-backup-strategy.bats: it opens `set -euo pipefail`, which the
# production image's busybox ash supports and the CI runner's dash rejects.

load test_helper

CHART=packages/system/backupstrategy-controller

# Render the shipped strategy's in-Pod script into $1 and substitute the
# controller-render placeholders the way the driver fills them: $2 mode, $3
# app (release and source application), $4 the archive name recorded on the
# Backup (empty for a Backup that records none), $5 the Backup name, $6 its
# takenAt in the archive-name layout. An empty render fails early, as does a
# placeholder left unsubstituted.
render_script() {
  out=$1; mode=$2; app=$3; archive=$4; backup=$5; takenat=$6
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

# Stub the clickhouse-backup API behind curl, first on PATH. list/remote
# serves $state/remote; actions serves $state/actions plus one success row per
# operation the script has issued, and after its first read swaps in
# $state/actions.next when a test seeds one (an operation that finishes while
# the script waits). Every request is logged to $state/curl.log. sleep is
# stubbed to fail after a few calls, so a script that never sees its
# operation finish fails the test instead of hanging it.
make_stubs() {
  bin=$1; state=$2
  mkdir -p "$bin"
  : > "$state/remote"
  : > "$state/actions"
  cat > "$bin/curl" <<STUB
#!/usr/bin/env bash
set -eu
method=GET; url=""
for a in "\$@"; do
  case "\$a" in POST) method=POST ;; http://*) url="\$a" ;; esac
done
echo "\$method \$url" >> "$state/curl.log"
case "\$method \$url" in
  "GET "*/backup/status) echo '{}' ;;
  "GET "*/backup/list/remote) cat "$state/remote" ;;
  "GET "*/backup/actions)
    cat "$state/actions"
    if [ -f "$state/actions.done" ]; then cat "$state/actions.done"; fi
    if [ -f "$state/actions.next" ]; then mv "$state/actions.next" "$state/actions"; fi
    ;;
  "POST "*/backup/create_remote*)
    name=\${url#*name=}; name=\${name%%&*}
    printf '{"command":"create_remote --resume %s","status":"success","start":"2099-01-01 00:00:00"}\n' "\$name" >> "$state/actions.done"
    ;;
  "POST "*/backup/restore_remote/*)
    printf '{"command":"restore_remote %s","status":"success","start":"2099-01-01 00:00:00"}\n' "\${url##*/}" >> "$state/actions.done"
    ;;
  *) echo "unexpected request: \$method \$url" >&2; exit 22 ;;
esac
STUB
  cat > "$bin/sleep" <<STUB
#!/usr/bin/env bash
n=\$(cat "$state/sleeps" 2>/dev/null || echo 0)
echo \$((n + 1)) > "$state/sleeps"
if [ "\$n" -ge 3 ]; then echo "sleep budget spent: the operation never finished" >&2; exit 1; fi
STUB
  chmod 0755 "$bin/curl" "$bin/sleep"
  export API_USERNAME=u API_PASSWORD=p
}

# One list/remote row per argument, "name" or "name|desc"; created mirrors
# the name's timestamp, which is the order the pre-fix selection sorted by.
remote_rows() {
  for row in "$@"; do
    name=${row%%|*}; desc="tar, regular"
    if [ "$row" != "$name" ]; then desc=${row#*|}; fi
    ts=${name##*-}
    printf '{"name":"%s","created":"%s","location":"remote","required":"","desc":"%s"}\n' "$name" "$ts" "$desc"
  done
}

@test "an empty render fails the fixture instead of passing as a no-op" {
  tmp=$(mktemp -d)
  CHART=$tmp/no-such-chart
  if render_script "$tmp/s.sh" restore myapp "" bkp 20260304T050622Z 2>/dev/null; then
    echo "FAIL: render_script reported success with no chart"
    false
  fi
  rm -rf "$tmp"
}

@test "restore reads back the archive the Backup recorded, not the newest one" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" restore myapp clickhouse-myapp-20260304T050607Z bkp-a 20260304T050622Z
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T050607Z \
    clickhouse-myapp-20260304T060000Z clickhouse-myapp-20260304T070000Z > "$tmp/remote"
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-myapp-20260304T050607Z' "$tmp/curl.log"
  [ "$(grep -c '/backup/restore_remote/' "$tmp/curl.log")" -eq 1 ]
  rm -rf "$tmp"
}

@test "restore fails naming the recorded archive when remote storage lacks it" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" restore myapp clickhouse-myapp-20260304T050607Z bkp-a 20260304T050622Z
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T060000Z > "$tmp/remote"
  out=""
  if out=$(PATH="$tmp/bin:$PATH" bash "$tmp/s.sh" 2>&1); then
    echo "FAIL: restore succeeded without the recorded archive: $out"
    false
  fi
  printf '%s\n' "$out" | grep -qF 'archive clickhouse-myapp-20260304T050607Z recorded on Backup bkp-a is not in remote storage'
  if grep -qF '/backup/restore_remote/' "$tmp/curl.log"; then
    echo "FAIL: restored another archive: $(cat "$tmp/curl.log")"
    false
  fi
  rm -rf "$tmp"
}

@test "restore of a Backup without a recorded archive takes the newest one stamped by its takenAt, and warns" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" restore myapp "" bkp-a 20260304T050800Z
  # Each decoy sorts after the expected archive and is ruled out by one
  # filter: broken, not a bare timestamp after the prefix, or stamped after
  # takenAt.
  remote_rows clickhouse-myapp-20260304T040000Z clickhouse-myapp-20260304T050607Z \
    "clickhouse-myapp-20260304T050700Z|broken (can't stat metadata.json)" \
    clickhouse-myapp-20260304T050710Z-copy \
    clickhouse-myapp-20260304T060000Z > "$tmp/remote"
  out=$(PATH="$tmp/bin:$PATH" bash "$tmp/s.sh" 2>&1)
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-myapp-20260304T050607Z' "$tmp/curl.log"
  [ "$(grep -c '/backup/restore_remote/' "$tmp/curl.log")" -eq 1 ]
  printf '%s\n' "$out" | grep -qF 'WARNING: Backup bkp-a records no archive name'
  rm -rf "$tmp"
}

@test "restore of a Backup without a recorded archive fails when no archive is stamped by its takenAt" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" restore myapp "" bkp-a 20260304T050800Z
  remote_rows clickhouse-myapp-20260304T060000Z clickhouse-myapp-20260304T070000Z > "$tmp/remote"
  out=""
  if out=$(PATH="$tmp/bin:$PATH" bash "$tmp/s.sh" 2>&1); then
    echo "FAIL: restore succeeded with every archive newer than takenAt: $out"
    false
  fi
  printf '%s\n' "$out" | grep -qF 'no archive of myapp stamped no later than 20260304T050800Z'
  if grep -qF '/backup/restore_remote/' "$tmp/curl.log"; then
    echo "FAIL: restored an archive newer than takenAt: $(cat "$tmp/curl.log")"
    false
  fi
  rm -rf "$tmp"
}

@test "backup writes the archive under the name the controller chose" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z "" ""
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/create_remote?name=clickhouse-myapp-20260304T050607Z&resume=true' "$tmp/curl.log"
  rm -rf "$tmp"
}

@test "a retried backup waits on the attempt the sidecar is still running" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z "" ""
  printf '{"command":"create_remote --resume clickhouse-myapp-20260304T050607Z","status":"in progress","start":"2026-03-04 05:06:08"}\n' > "$tmp/actions"
  printf '{"command":"create_remote --resume clickhouse-myapp-20260304T050607Z","status":"success","start":"2026-03-04 05:06:08"}\n' > "$tmp/actions.next"
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  # The sidecar answers a second operation with 423 while one runs.
  if grep -qF 'POST ' "$tmp/curl.log"; then
    echo "FAIL: started another operation over the running one: $(cat "$tmp/curl.log")"
    false
  fi
  rm -rf "$tmp"
}

@test "a retried backup after a failed attempt resumes the same archive" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" backup myapp clickhouse-myapp-20260304T050607Z "" ""
  printf '{"command":"create_remote clickhouse-myapp-20260304T050607Z","status":"error","start":"2026-03-04 05:06:08","error":"S3 PutObject 403"}\n' > "$tmp/actions"
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  grep -qxF 'POST http://chi-clickhouse-myapp-clickhouse-0-0.tenant-x.svc:7171/backup/create_remote?name=clickhouse-myapp-20260304T050607Z&resume=true' "$tmp/curl.log"
  rm -rf "$tmp"
}

@test "backup falls back to a timestamped name when the controller supplies none" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp"
  render_script "$tmp/s.sh" backup myapp "" "" ""
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  grep -qE '^POST http://[^ ]*/backup/create_remote\?name=clickhouse-myapp-[0-9]{8}T[0-9]{6}Z&resume=true$' "$tmp/curl.log"
  rm -rf "$tmp"
}
