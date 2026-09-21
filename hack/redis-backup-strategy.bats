#!/usr/bin/env bats
# Behavioural tests for the shipped Redis backup/restore strategy script,
# extracted from the rendered cozy-default-redis Strategy CR and EXECUTED with
# stubbed redis-cli / curl. helm-unittest (tests/strategy_redis_test.yaml) pins
# the chart's render-time decisions (volumes, env, deadline); the script's own
# behaviour — which mode reaches which command — is a contract between the
# running Pod, Redis and S3, so it is exercised here rather than string-matched.
#
# The load-bearing case is cleanup: a deleted Backup renders MODE=cleanup, and
# it must delete the object and NEVER reach the restore path's FLUSHALL against
# the live master. Blanking the object key or breaking the rfs-redis- sentinel
# prefix is caught the same way — by what the stubs are actually called with.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. Each @test builds its own fixture; a
# load-bearing assertion is never the last line (cozytest.sh rewrites a lone
# `}` into `return 0` + `}`, which would swallow the final command's status).
#
# The rendered script is run with `bash`, not `sh`: it opens `set -euo
# pipefail`, which the production image's busybox ash supports but the CI
# runner's /bin/sh (dash) rejects at line 1. bash matches ash's pipefail
# semantics; same reason hack/migration-50-etcd-adopt.bats runs its script
# with bash.

CHART=packages/system/backupstrategy-controller

# Render the shipped strategy's in-Pod script into $1, substitute the three
# controller-render placeholders (Mode, Release.Name, ObjectKey) with the given
# values ($2 mode, $3 app, $4 object key), and rebind every absolute path it
# touches into $5 so it runs unprivileged. An empty render is an early failure,
# and no real /etc or /tmp path may survive the rebinding — these tests EXECUTE
# the script.
render_script() {
  out=$1; mode=$2; app=$3; key=$4; sandbox=$5
  mkdir -p "$sandbox/redis-ca" "$sandbox/s3-ca" "$sandbox/restore"
  helm template t "$CHART" -n cozy-backup-controller \
    --set backupStorage.bucketNameOverride=test-bucket 2>/dev/null \
    | yq eval 'select(.kind == "Redis") | .spec.template.spec.containers[0].args[0]' - \
    | sed -e "s#{{ .Mode }}#${mode}#g" \
          -e "s#{{ .Release.Name }}#${app}#g" \
          -e "s#{{ .ObjectKey }}#${key}#g" \
          -e "s#/etc/redis-ca#${sandbox}/redis-ca#g" \
          -e "s#/etc/s3-ca#${sandbox}/s3-ca#g" \
          -e "s#/tmp/restore#${sandbox}/restore#g" \
          -e "s#/tmp/dump.rdb#${sandbox}/dump.rdb#g" > "$out"
  [ -s "$out" ] || return 1
  # The exact absolute paths the rebinding above replaces; a survivor means the
  # script grew a new one and the test would touch the real filesystem. Matched
  # literally (not a bare /tmp/, which the sandbox itself lives under in CI).
  if grep -qE '/etc/redis-ca|/etc/s3-ca|/tmp/dump\.rdb|/tmp/restore' "$out"; then
    echo "rendered script still references a real /etc or /tmp path after rebinding" >&2
    return 1
  fi
}

# redis-cli / curl stubs that log every invocation, put first on PATH. The
# sentinel query answers with a master addr so backup/restore proceed; nothing
# actually talks to Redis or S3.
make_stubs() {
  bin=$1; rclog=$2; curllog=$3
  mkdir -p "$bin"
  cat > "$bin/redis-cli" <<STUB
#!/bin/sh
echo "\$*" >> "$rclog"
case "\$*" in
  *"get-master-addr-by-name"*) printf '127.0.0.1\n6379\n' ;;
  *"INFO persistence"*) printf 'aof_rewrite_in_progress:0\naof_last_bgrewrite_status:ok\n' ;;
esac
exit 0
STUB
  cat > "$bin/curl" <<STUB
#!/bin/sh
echo "\$*" >> "$curllog"
# Cleanup asks for the HTTP status with -w; a 204 is a successful delete.
case "\$*" in *"-w %{http_code}"*|*"-w"*) echo 204 ;; esac
exit 0
STUB
  cat > "$bin/redis-server" <<'STUB'
#!/bin/sh
exit 0
STUB
  chmod 0755 "$bin/redis-cli" "$bin/curl" "$bin/redis-server"
}

s3_env() {
  export S3_ENDPOINT=s3.example.com S3_BUCKET=bkt S3_REGION=us-east-1
  export AWS_ACCESS_KEY_ID=ak AWS_SECRET_ACCESS_KEY=sk S3_INSECURE_SKIP_VERIFY=false
}

@test "an empty render fails the fixture instead of passing as a no-op" {
  tmp=$(mktemp -d)
  CHART=$tmp/no-such-chart
  if render_script "$tmp/s.sh" cleanup app key "$tmp/sb" 2>/dev/null; then
    echo "FAIL: render_script reported success with no chart"
    false
  fi
}

@test "cleanup deletes the object and never FLUSHALLs the live master" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/rc.log" "$tmp/curl.log"
  render_script "$tmp/s.sh" cleanup myapp "tenant-x/myapp/bkp-42.rdb" "$tmp/sb"
  s3_env
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  # A DELETE against exactly this backup's object.
  grep -q -- '-X DELETE' "$tmp/curl.log"
  grep -q 'tenant-x/myapp/bkp-42.rdb' "$tmp/curl.log"
  # The destructive branch must be unreachable in cleanup: no FLUSHALL, no
  # --pipe, and in fact no Redis contact at all. This is the assertion the
  # round's cleanup arm exists to hold; it must not be the last line.
  ! grep -qi 'FLUSHALL' "$tmp/rc.log"
  ! grep -q -- '--pipe' "$tmp/rc.log"
  [ ! -s "$tmp/rc.log" ]
}

@test "backup resolves rfs-redis-<app> and uploads under the recorded object key" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/rc.log" "$tmp/curl.log"
  render_script "$tmp/s.sh" backup myapp "tenant-x/myapp/bkp-42.rdb" "$tmp/sb"
  s3_env
  PATH="$tmp/bin:$PATH" bash "$tmp/s.sh"
  # Sentinel host carries the rfs-redis- prefix owned by the redis chart.
  grep -q -- '-h rfs-redis-myapp -p 26379' "$tmp/rc.log"
  grep -q 'get-master-addr-by-name mymaster' "$tmp/rc.log"
  # The RDB is uploaded to exactly the recorded object key, so a Plan keeps one
  # object per run.
  grep -q -- '-X PUT' "$tmp/curl.log"
  grep -q 'tenant-x/myapp/bkp-42.rdb' "$tmp/curl.log"
  # Backup must never wipe the source.
  ! grep -qi 'FLUSHALL' "$tmp/rc.log"
}

@test "an unrecognised mode exits non-zero without touching the master" {
  tmp=$(mktemp -d)
  make_stubs "$tmp/bin" "$tmp/rc.log" "$tmp/curl.log"
  render_script "$tmp/s.sh" bogus myapp "tenant-x/myapp/bkp-42.rdb" "$tmp/sb"
  s3_env
  # No bats `run` helper under hack/cozytest.sh, so capture the status directly.
  if PATH="$tmp/bin:$PATH" bash "$tmp/s.sh" 2>/dev/null; then
    echo "FAIL: an unrecognised mode must exit non-zero"
    false
  fi
  # The unknown mode must fail closed, never reaching the restore FLUSHALL.
  ! grep -qi 'FLUSHALL' "$tmp/rc.log"
  [ -f "$tmp/rc.log" ]
}
