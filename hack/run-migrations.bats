#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for packages/core/platform/images/migrations/run-migrations.sh.
#
# The migration hook Job retries a failed pod with the environment rendered for
# the first one, so CURRENT_VERSION is what the cluster was on before any
# migration of this upgrade ran. Every migration that finished has stamped the
# cozystack-version ConfigMap since, and a retried pod must start from that
# stamp, or it runs finished migrations a second time.
#
# The runner executes in the migrations image's alpine base (busybox ash, the
# interpreter it ships with) against stand-in migrations and a fake kubectl
# that keeps the stamped version in a file, so one pod's stamps are what the
# next pod reads. The harness shape is hack/migration-58-http-cache-freeze.bats's.
#
# Run with: hack/cozytest.sh hack/run-migrations.bats
# -----------------------------------------------------------------------------

FAKEBIN="$PWD/hack/testdata/run-migrations"
RUNNER="$PWD/packages/core/platform/images/migrations/run-migrations.sh"
LIB="$PWD/packages/core/platform/images/migrations/migrations/lib"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
WORKROOT="${TMPDIR:-/tmp}/cozy-run-migrations-$$"

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

# prep <stamped version, or "none" for no ConfigMap>
prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run run-migrations.sh inside $ALPINE," >&2
    echo "the migrations image's base." >&2
    return 1
  }
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  mkdir -p "$WORK/migrations"
  cp -R "$LIB" "$WORK/migrations/lib"
  for n in 57 58 59; do
    cp "$FAKEBIN/migration" "$WORK/migrations/$n"
  done
  : > "$WORK/ran"
  [ "$1" = none ] || printf '%s' "$1" > "$WORK/state"
  chmod +x "$FAKEBIN/kubectl"
  unset FAKE_FAIL_AT FAKE_READ_FAIL FAKE_EXIST_FAIL || true
  return 0
}

# run_pod <CURRENT_VERSION> <TARGET_VERSION> -- one pod of the hook Job.
run_pod() {
  _run_pod_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$RUNNER:/usr/bin/run-migrations.sh:ro" \
    -v "$WORK/migrations:/migrations" \
    -v "$FAKEBIN:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_STATE=/work/state \
    -e FAKE_FAIL_AT="${FAKE_FAIL_AT-}" \
    -e FAKE_READ_FAIL="${FAKE_READ_FAIL-}" \
    -e FAKE_EXIST_FAIL="${FAKE_EXIST_FAIL-}" \
    -e NAMESPACE=cozy-system \
    -e CURRENT_VERSION="$1" \
    -e TARGET_VERSION="$2" \
    "$ALPINE" /bin/sh /usr/bin/run-migrations.sh || _run_pod_rc=$?
  return "$_run_pod_rc"
}

ran() {
  tr '\n' ' ' < "$WORK/ran" | sed 's/ $//'
  return 0
}

@test "a retried pod starts from the version the failed pod stamped" {
  prep 57
  export FAKE_FAIL_AT=59
  rc=0
  run_pod 57 60 || rc=$?
  [ "$rc" -ne 0 ]
  [ "$(cat "$WORK/state")" = 59 ]

  unset FAKE_FAIL_AT
  rc=0
  run_pod 57 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -eq 0 ]
  [ "$(ran)" = "57 58 59 59" ]
  [ "$(cat "$WORK/state")" = 60 ]
  rm -rf "$WORK"
}

@test "a pod that finds the target already stamped runs nothing" {
  prep 60
  rc=0
  run_pod 57 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -eq 0 ]
  [ -z "$(ran)" ]
  rm -rf "$WORK"
}

@test "a ConfigMap with no version falls back to the rendered one" {
  prep ""
  rc=0
  run_pod 58 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -eq 0 ]
  [ "$(ran)" = "58 59" ]
  rm -rf "$WORK"
}

# A non-integer start fails the -ge test inside an if and makes seq fail
# inside the for list, and neither trips set -e: without the fallback such a
# stamp gives a green Job that ran nothing and moved no stamp.
@test "a ConfigMap with a non-integer version falls back to the rendered one" {
  prep v58
  rc=0
  run_pod 58 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -eq 0 ]
  [ "$(ran)" = "58 59" ]
  rm -rf "$WORK"
}

# The same unguarded -ge and empty seq turn an all-digit stamp past the shell's
# integer range into a green Job that ran nothing. Falling back is no answer:
# the chart's int renders such a stamp as 0, which would replay every migration.
@test "a stamped version outside the shell's integer range stops before any migration" {
  prep 999999999999999999999999999999
  rc=0
  run_pod 0 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -ne 0 ]
  [ -z "$(ran)" ]
  rm -rf "$WORK"
}

@test "a failed read of the stamped version stops before any migration" {
  prep 58
  export FAKE_READ_FAIL="Error from server (Timeout): the server was unable to return a response in the time allotted"
  rc=0
  run_pod 57 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -ne 0 ]
  [ -z "$(ran)" ]
  [ "$(cat "$WORK/state")" = 58 ]
  rm -rf "$WORK"
}

# Only NotFound means a fresh cluster. Reading any other failure of the
# existence check as absence stamps the target over a cluster whose
# migrations never ran, and the Job ends green.
@test "a failed existence check stops before stamping or running anything" {
  prep 57
  export FAKE_EXIST_FAIL="Unable to connect to the server: net/http: TLS handshake timeout"
  rc=0
  run_pod 57 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -ne 0 ]
  [ -z "$(ran)" ]
  [ "$(cat "$WORK/state")" = 57 ]
  rm -rf "$WORK"
}

@test "with no ConfigMap it stamps the target and runs nothing" {
  prep none
  rc=0
  run_pod 57 60 || rc=$?
  echo "ran: $(ran)"
  [ "$rc" -eq 0 ]
  [ -z "$(ran)" ]
  [ "$(cat "$WORK/state")" = 60 ]
  rm -rf "$WORK"
}
