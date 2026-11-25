#!/usr/bin/env bats
# Behavioural tests for the example ClickHouse (Altinity) strategy that
# examples/backups/clickhouse/01-create-strategy.sh applies. The script runs
# against a stubbed kubectl, which captures the Strategy it applies; the
# in-Pod script of that Strategy is then rendered the way the backup
# controller renders it and EXECUTED against a stubbed clickhouse-backup
# sidecar API.
#
# The contract pinned here is which archive the example touches: the one the
# controller named for the run (.ArchiveName), so that the Backup records the
# archive the run wrote, its restore reads that archive back and deleting it
# removes that archive. The name the script mints from the clock, and the
# newest archive of the source application, are only the fallbacks for a run
# the controller gave no name.

load test_helper

EXAMPLE=examples/backups/clickhouse/01-create-strategy.sh

# Capture the Strategy the example applies into $1, with kubectl stubbed so
# no cluster is touched: `get` answers with a client image, so the example
# does not fall back to installing tools at run time, and `apply -f -` writes
# its manifest.
capture_strategy() {
  out=$1
  stub=$(mktemp -d)
  cat > "$stub/kubectl" <<STUB
#!/bin/bash
case "\$1" in
  get) echo "client-image:test" ;;
  apply) cat > "$out" ;;
esac
STUB
  chmod +x "$stub/kubectl"
  PATH="$stub:$PATH" bash "$EXAMPLE" >/dev/null 2>&1
  [ -s "$out" ] || return 1
}

# Render the captured Strategy's in-Pod script into $2 as the controller
# would: $3 mode, $4 the application (release and source), $5 the archive
# name the controller chose or the Backup recorded (empty for none).
render_example() {
  manifest=$1; out=$2; mode=$3; app=$4; archive=${5-}
  yq eval '.spec.template.spec.containers[0].args[0]' "$manifest" \
    | sed -e "s#{{ .Mode }}#${mode}#g" \
          -e "s#{{ .Release.Name }}#${app}#g" \
          -e "s#{{ .Release.Namespace }}#tenant-x#g" \
          -e "s#{{ .Backup.ApplicationRef.Name }}#${app}#g" \
          -e "s#{{ .ArchiveName | default \"\" }}#${archive}#g" > "$out"
  [ -s "$out" ] || return 1
  if grep -q '{{' "$out"; then
    echo "rendered script still holds a controller placeholder" >&2
    return 1
  fi
}

# A curl stub standing in for the sidecar, first on PATH next to a no-op
# sleep. Every request is logged to $2/requests; an accepted POST makes the
# action log report that operation as succeeded; list/remote lists two
# archives of app "ch", the newer one second.
make_sidecar() {
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
case "\$method \$url" in
  "POST "*create_remote\?name=*) name=\${url#*name=}; echo "create_remote \$name" > "\$state/op" ;;
  "POST "*/restore_remote/*) echo "restore_remote \${url##*/}" > "\$state/op" ;;
  "GET "*/backup/actions) [ -s "\$state/op" ] && printf '{"command":"%s","status":"success","start":"2026-01-01 00:00:00"}\n' "\$(cat "\$state/op")" ;;
  "GET "*/backup/list/remote)
    printf '{"name":"clickhouse-ch-20260101T000000Z","created":"2026-01-01 00:00:00"}\n'
    printf '{"name":"clickhouse-ch-20260102T000000Z","created":"2026-01-02 00:00:00"}\n'
    ;;
esac
exit 0
STUB
  printf '#!/bin/sh\nexit 0\n' > "$bin/sleep"
  chmod +x "$bin/curl" "$bin/sleep"
}

setup() {
  strict_setup
  work=$(mktemp -d)
  capture_strategy "$work/strategy.yaml"
  make_sidecar "$work/bin" "$work/state"
}

run_example() {
  render_example "$work/strategy.yaml" "$work/script.sh" "$1" ch "${2-}"
  API_USERNAME=u API_PASSWORD=p PATH="$work/bin:$PATH" bash "$work/script.sh"
}

@test "backup uploads under the archive name the controller chose" {
  run_example backup ch-20260304050607-0f3c9a1e5b7d
  if ! grep -qx 'POST http://chi-clickhouse-ch-clickhouse-0-0.tenant-x.svc:7171/backup/create_remote?name=ch-20260304050607-0f3c9a1e5b7d' "$work/state/requests"; then
    echo "backup did not upload under the controller's name:" >&2
    cat "$work/state/requests" >&2
    exit 1
  fi
}

@test "backup without a controller name falls back to a name from the clock" {
  run_example backup ""
  if ! grep -qE '^POST .*/backup/create_remote\?name=clickhouse-ch-[0-9]{8}T[0-9]{6}Z$' "$work/state/requests"; then
    echo "backup without a name did not mint one from the clock:" >&2
    cat "$work/state/requests" >&2
    exit 1
  fi
}

@test "restore reads back the archive the Backup recorded, not the newest one" {
  run_example restore clickhouse-ch-20260101T000000Z
  if ! grep -qx 'POST http://chi-clickhouse-ch-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-ch-20260101T000000Z' "$work/state/requests"; then
    echo "restore did not read the recorded archive:" >&2
    cat "$work/state/requests" >&2
    exit 1
  fi
}

@test "restore of a Backup that records no archive takes the newest one" {
  run_example restore ""
  if ! grep -qx 'POST http://chi-clickhouse-ch-clickhouse-0-0.tenant-x.svc:7171/backup/restore_remote/clickhouse-ch-20260102T000000Z' "$work/state/requests"; then
    echo "restore without a recorded archive did not take the newest one:" >&2
    cat "$work/state/requests" >&2
    exit 1
  fi
}
