#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 60 --> 61 (move the Strimzi custom
# resources to the v1 API, refuse ZooKeeper-based Kafkas, move Kafka 3.x
# applications to v4.3). See
# packages/core/platform/images/migrations/migrations/60 for the mechanism.
#
# The fake kubectl keeps the storedVersions every Strimzi CRD reports in a
# state file, and the conversion Job rewrites it when it succeeds, so the
# check after the Job reads what the Job did.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-60-strimzi-v1-conversion.bats
# -----------------------------------------------------------------------------

load test_helper

FIXTURES="$PWD/hack/testdata/migration-60-strimzi-v1"
FAKE_FIXTURES="$FIXTURES"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration60-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-60-$$"

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

# The wait for the conversion Job counts only its sleeps, so a zero poll
# interval would let a wait that misses the end of the Job spin for good. A
# one-second poll under a three-second timeout turns that into a quick failure.
run_migration() {
  _run_migration_rc=0
  docker run --rm --network none \
    --user "$(id -u):$(id -g)" \
    -v "$MIG_DIR:/migrations:ro" \
    -v "$FIXTURES:/fakebin:ro" \
    -v "$WORK:/work" \
    -e PATH=/fakebin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e FAKE_CMDLOG=/work/cmdlog \
    -e FAKE_STATE=/work/state \
    -e FAKE_APPS=/fakebin/apps.txt \
    -e FAKE_RELEASES=/fakebin/kafka-releases.txt \
    -e FAKE_KAFKAS="/fakebin/${FAKE_KAFKAS_FILE:-kafkas-kraft.json}" \
    -e FAKE_NO_STRIMZI="${FAKE_NO_STRIMZI-}" \
    -e FAKE_JOB="${FAKE_JOB-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    -e FAKE_MANAGED_CONFLICT="${FAKE_MANAGED_CONFLICT-}" \
    -e FAKE_REBALANCES="${FAKE_REBALANCES-}" \
    -e FAKE_CRD_READ_FAIL="${FAKE_CRD_READ_FAIL-}" \
    -e FAKE_APIRES_FAIL="${FAKE_APIRES_FAIL-}" \
    -e FAKE_SECRET_LIST_FAIL="${FAKE_SECRET_LIST_FAIL-}" \
    -e FAKE_RELEASE_REWRITTEN_BACK="${FAKE_RELEASE_REWRITTEN_BACK-}" \
    -e FAKE_OLD_JOB="${FAKE_OLD_JOB-}" \
    -e FAKE_REBALANCE_FAIL="${FAKE_REBALANCE_FAIL-}" \
    -e FAKE_OPERATOR_READ_FAIL="${FAKE_OPERATOR_READ_FAIL-}" \
    -e FAKE_SERVED="${FAKE_SERVED-}" \
    -e FAKE_MANAGED_PATCH_FAIL="${FAKE_MANAGED_PATCH_FAIL-}" \
    -e FAKE_PULL="${FAKE_PULL-}" \
    -e STRIMZI_CRD_FILE="${STRIMZI_CRD_FILE-/fakebin/strimzi-crds-0.51.0.yaml}" \
    -e POLL_INTERVAL=1 \
    -e CONVERSION_TIMEOUT="${CONVERSION_TIMEOUT-3}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 60 inside a jq-enabled" >&2
    echo "build of $ALPINE, the migrations image's base." >&2
    return 1
  }
  docker build -q -t "$TESTIMG" - >/dev/null <<DOCKERFILE
FROM $ALPINE
RUN apk add --no-cache jq
DOCKERFILE
  chmod +x "$FIXTURES/kubectl"
  mkdir -p "$WORKROOT"
  WORK=$(mktemp -d "$WORKROOT/XXXXXX")
  mkdir -p "$WORK/state"
  printf '["v1beta2"]' > "$WORK/state/stored"
  FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  unset FAKE_NO_STRIMZI FAKE_JOB FAKE_PATCH_FAIL FAKE_KAFKAS_FILE FAKE_MANAGED_CONFLICT FAKE_MANAGED_PATCH_FAIL FAKE_REBALANCES STRIMZI_CRD_FILE FAKE_CRD_READ_FAIL FAKE_APIRES_FAIL FAKE_SECRET_LIST_FAIL FAKE_RELEASE_REWRITTEN_BACK FAKE_OLD_JOB FAKE_REBALANCE_FAIL FAKE_OPERATOR_READ_FAIL FAKE_SERVED FAKE_PULL CONVERSION_TIMEOUT || true
  return 0
}

release_json() {
  base64 -d < "$WORK/state/release-$1" | base64 -d | gzip -dc
}

@test "converts to v1, moves Kafka 3.x applications to v4.3 and stamps 61" {
  prep
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  grep -qxF "APPLY-CRDS /fakebin/strimzi-crds-0.51.0.yaml" "$FAKE_CMDLOG"
  # convert-resource is the init container, so crd-upgrade cannot start first.
  grep -qxF "APPLY-CONVERSION convert-resource crd-upgrade" "$FAKE_CMDLOG"
  crds_line=$(grep -nxF "APPLY-CRDS /fakebin/strimzi-crds-0.51.0.yaml" "$FAKE_CMDLOG" | cut -d: -f1)
  job_line=$(grep -nxF "APPLY-CONVERSION convert-resource crd-upgrade" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$crds_line" -lt "$job_line" ]
  # The 0.45 operator is stopped before anything is converted.
  scale_line=$(grep -nxF "SCALE-OPERATOR 0" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$scale_line" -lt "$crds_line" ]
  # The entity operators only after the cluster operator, or it scales them back.
  entity_line=$(grep -nxF "SCALE-ENTITY tenant-a/kafka-one-entity-operator" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$scale_line" -lt "$entity_line" ]
  [ "$entity_line" -lt "$crds_line" ]

  grep -qxF 'PATCH tenant-a/one {"spec":{"version":"v4.3"}}' "$FAKE_CMDLOG"
  grep -qxF 'PATCH tenant-b/two {"spec":{"version":"v4.3"}}' "$FAKE_CMDLOG"
  if grep -qE "^PATCH tenant-(c|d)/" "$FAKE_CMDLOG"; then echo "patched an application that needs no version change" >&2; return 1; fi

  grep -qxF "DELETE clusterrole strimzi-v1-api-conversion" "$FAKE_CMDLOG"
  [ "$(tail -1 "$FAKE_CMDLOG")" = "STAMP 61" ]
  rm -rf "$WORK"
}

# Helm builds the objects of the deployed release before it upgrades or
# uninstalls it, and fails on an API version the server no longer serves.
@test "rewrites the deployed Helm release of each Kafka to the v1 API" {
  prep
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  grep -qxF "PATCH-RELEASE tenant-a/sh.helm.release.v1.kafka-one.v3" "$FAKE_CMDLOG"
  grep -qxF "PATCH-RELEASE tenant-a/sh.helm.release.v1.kafka-one.v4" "$FAKE_CMDLOG"
  # An application whose install failed has a release and no Kafka CR.
  grep -qxF "PATCH-RELEASE tenant-f/sh.helm.release.v1.kafka-failed-install.v1" "$FAKE_CMDLOG"
  # The applications leave Kafka 3.x before any release is rewritten.
  version_line=$(grep -nF 'PATCH tenant-a/one ' "$FAKE_CMDLOG" | cut -d: -f1)
  first_release_line=$(grep -nF 'PATCH-RELEASE ' "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
  [ "$version_line" -lt "$first_release_line" ]
  release_json v4 > "$WORK/release.json"
  [ "$(jq -r '.manifest' "$WORK/release.json" | grep -c '^apiVersion: kafka.strimzi.io/v1$')" -eq 2 ]
  if jq -r '.manifest' "$WORK/release.json" | grep -qF 'kafka.strimzi.io/v1beta2'; then echo "v1beta2 left in the release manifest" >&2; return 1; fi
  # Only the manifest is rewritten; everything else in the release stays as it was.
  jq 'del(.manifest)' "$FAKE_FIXTURES/release-kafka-one.json" > "$WORK/want"
  jq 'del(.manifest)' "$WORK/release.json" > "$WORK/got"
  cmp "$WORK/want" "$WORK/got"
  release_line=$(grep -nxF "PATCH-RELEASE tenant-a/sh.helm.release.v1.kafka-one.v3" "$FAKE_CMDLOG" | cut -d: -f1)
  stamp_line=$(grep -nxF "STAMP 61" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$release_line" -lt "$stamp_line" ]

  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  if grep -qF "PATCH-RELEASE" "$FAKE_CMDLOG"; then echo "a second run rewrote the release again" >&2; return 1; fi
  rm -rf "$WORK"
}

# Once the CRDs serve only v1, the API server refuses a server-side apply to
# an object whose managedFields still name v1beta2.
@test "moves managedFields entries recorded under old API versions to v1" {
  prep
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qxF "PATCH-MANAGED tenant-a/kafka-one rv=100 helm-controller=kafka.strimzi.io/v1,strimzi-cluster-operator=kafka.strimzi.io/v1" "$FAKE_CMDLOG"
  # The status entry keeps its subresource and its fields.
  [ "$(jq -r '.items[0].metadata.managedFields[1].subresource' "$WORK/state/managed")" = "status" ]
  if grep -qF "PATCH-MANAGED tenant-b/" "$FAKE_CMDLOG"; then echo "patched an object already on v1" >&2; return 1; fi

  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ]
  if grep -qF "PATCH-MANAGED" "$FAKE_CMDLOG"; then echo "a second run patched managedFields again" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a managedFields patch that loses a race is rebuilt from a fresh read" {
  prep
  export FAKE_MANAGED_CONFLICT=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qxF "PATCH-MANAGED tenant-a/kafka-one rv=100 helm-controller=kafka.strimzi.io/v1,strimzi-cluster-operator=kafka.strimzi.io/v1" "$FAKE_CMDLOG"
  grep -qxF "PATCH-MANAGED tenant-a/kafka-one rv=101 helm-controller=kafka.strimzi.io/v1,strimzi-cluster-operator=kafka.strimzi.io/v1" "$FAKE_CMDLOG"
  [ "$(jq -r '.items[0].metadata.managedFields[0].apiVersion' "$WORK/state/managed")" = "kafka.strimzi.io/v1" ]
  grep -qxF "STAMP 61" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a managedFields patch that fails for another reason aborts before stamping" {
  prep
  export FAKE_MANAGED_PATCH_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  [ "$(grep -c "PATCH-MANAGED" "$FAKE_CMDLOG")" -eq 1 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed managedFields patch" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a cluster without Strimzi only stamps the version" {
  prep
  export FAKE_NO_STRIMZI=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "APPLY-CRDS|APPLY-CONVERSION|PATCH" "$FAKE_CMDLOG"; then echo "touched Strimzi on a cluster without it" >&2; return 1; fi
  grep -qxF "STAMP 61" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a ZooKeeper-based or still-migrating Kafka stops the migration before it changes anything" {
  prep
  export FAKE_KAFKAS_FILE=kafkas-zk.json
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qxF "  tenant-c/kafka-zk" "$WORK/out"
  grep -qxF "  tenant-d/kafka-mid" "$WORK/out"
  if grep -qF "tenant-a/kafka-one" "$WORK/out"; then echo "named a KRaft cluster as ZooKeeper-based" >&2; return 1; fi
  grep -qF "Cozystack v1.7" "$WORK/out"
  if grep -qE "APPLY-CRDS|APPLY-CONVERSION|PATCH|STAMP|SCALE-OPERATOR" "$FAKE_CMDLOG"; then echo "changed the cluster despite a ZooKeeper Kafka" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a drain of the v1.7 ZooKeeper migration still in flight stops the migration" {
  prep
  export FAKE_REBALANCES="tenant-a/kafka-one-drain-kafka,tenant-a/my-own-rebalance"
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qxF "  tenant-a/kafka-one-drain-kafka" "$WORK/out"
  if grep -qxF "  tenant-a/my-own-rebalance" "$WORK/out"; then echo "named an unrelated KafkaRebalance" >&2; return 1; fi
  if grep -qE "SCALE-OPERATOR|APPLY-CRDS|STAMP" "$FAKE_CMDLOG"; then echo "changed the cluster despite an unfinished drain" >&2; return 1; fi
  rm -rf "$WORK"
}

# Run by hand outside the migrations image, the CRD file is easy to miss;
# the operators must not be stopped for a run that cannot go on.
@test "a missing CRD file aborts before the operators are stopped" {
  prep
  export STRIMZI_CRD_FILE=/nonexistent/strimzi-crds-0.51.0.yaml
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "/nonexistent/strimzi-crds-0.51.0.yaml is not readable" "$WORK/out"
  if grep -qE "SCALE-|APPLY-CRDS|STAMP" "$FAKE_CMDLOG"; then echo "changed the cluster without the CRD file" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed conversion Job aborts with its logs, before any application changes" {
  prep
  export FAKE_JOB=fail
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "fake conversion log" "$WORK/out"
  # The failure ends the wait at once, not at the timeout.
  [ "$(grep -cF "get job strimzi-v1-api-conversion -o jsonpath={.status.succeeded}" "$FAKE_CMDLOG")" -eq 1 ]
  if grep -qE "PATCH|STAMP" "$FAKE_CMDLOG"; then echo "went on after the conversion failed" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a Job that leaves a non-v1 stored version behind aborts" {
  prep
  export FAKE_JOB=noop
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF 'kafkas.kafka.strimzi.io: ["v1beta2"]' "$WORK/out"
  if grep -qE "PATCH|STAMP" "$FAKE_CMDLOG"; then echo "went on with v1beta2 still stored" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a re-run after the conversion skips it and still moves the versions" {
  prep
  printf '["v1"]' > "$WORK/state/stored"
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "APPLY-CRDS|APPLY-CONVERSION|kafkas.v1beta2" "$FAKE_CMDLOG"; then echo "converted again" >&2; return 1; fi
  # The CRDs still serve v1beta2, so the operator may be 0.45: it is stopped.
  grep -qxF "SCALE-OPERATOR 0" "$FAKE_CMDLOG"
  grep -qxF 'PATCH tenant-a/one {"spec":{"version":"v4.3"}}' "$FAKE_CMDLOG"
  grep -qxF "STAMP 61" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing application patch aborts before stamping" {
  prep
  export FAKE_PATCH_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed patch" >&2; return 1; fi
  rm -rf "$WORK"
}

# The migration runs once: a read that fails must stop it before the stamp,
# never pass for "nothing there".
@test "a failed read of the CRD stored versions aborts before stamping" {
  prep
  export FAKE_CRD_READ_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "STAMP|SCALE-|APPLY-CRDS|PATCH" "$FAKE_CMDLOG"; then echo "went on after a failed CRD read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed read of the API resources aborts before stamping" {
  prep
  export FAKE_APIRES_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "STAMP|PATCH-RELEASE" "$FAKE_CMDLOG"; then echo "went on after a failed API resources read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed Helm release listing aborts before stamping" {
  prep
  export FAKE_SECRET_LIST_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped after a failed release listing" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a release that names v1beta2 again after the rewrite stops the stamp" {
  prep
  export FAKE_RELEASE_REWRITTEN_BACK=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qxF "  Helm release tenant-a/sh.helm.release.v1.kafka-one.v3" "$WORK/out"
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "stamped with a v1beta2 release left" >&2; return 1; fi
  rm -rf "$WORK"
}

# A Job failed on its deadline can still have a pod terminating, and
# convert-resource writes until that pod is gone.
@test "a finished Job of an earlier attempt is removed with its pods before the CRDs are touched" {
  for state in failed succeeded; do
    prep
    export FAKE_OLD_JOB=$state
    rc=0
    run_migration 60 >"$WORK/out" 2>&1 || rc=$?
    cat "$WORK/out"; cat "$FAKE_CMDLOG"
    [ "$rc" -eq 0 ]
    delete_line=$(grep -nxF "DELETE job strimzi-v1-api-conversion foreground" "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
    scale_line=$(grep -nxF "SCALE-OPERATOR 0" "$FAKE_CMDLOG" | cut -d: -f1)
    crds_line=$(grep -nF "APPLY-CRDS" "$FAKE_CMDLOG" | cut -d: -f1)
    job_line=$(grep -nF "APPLY-CONVERSION" "$FAKE_CMDLOG" | cut -d: -f1)
    [ "$delete_line" -lt "$scale_line" ]
    [ "$delete_line" -lt "$crds_line" ]
    [ "$crds_line" -lt "$job_line" ]
    rm -rf "$WORK"
  done
}

@test "a Job of an earlier attempt that fails while waited for is removed with its pods and started over" {
  prep
  export FAKE_OLD_JOB=running FAKE_JOB=fail
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  # The new Job fails as well, so the run stops after starting it.
  [ "$rc" -ne 0 ]
  # Each failure ends its wait at once, not at the timeout.
  [ "$(grep -cF "get job strimzi-v1-api-conversion -o jsonpath={.status.succeeded}" "$FAKE_CMDLOG")" -eq 2 ]
  delete_line=$(grep -nxF "DELETE job strimzi-v1-api-conversion foreground" "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
  crds_line=$(grep -nF "APPLY-CRDS" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$delete_line" -lt "$crds_line" ]
  grep -qF "APPLY-CONVERSION" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

# Without a deadline, a Job whose pod never starts never fails, and every
# retry waits on it and exits. The deadline must cover the wait of the run
# that started the Job plus the wait of the retry after it.
@test "the conversion Job carries a deadline of at least two waits for it" {
  prep
  export CONVERSION_TIMEOUT=100
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  deadline=$(sed -n 's/^CONVERSION-DEADLINE //p' "$FAKE_CMDLOG")
  [ -n "$deadline" ]
  [ "$deadline" -ge 200 ]
  rm -rf "$WORK"
}

# A pod the API server refused never fails, so the Job reports its deadline
# only in its Failed condition.
@test "a Job of an earlier attempt failed on its deadline without a pod is started over" {
  prep
  export FAKE_OLD_JOB=running FAKE_JOB=nopod CONVERSION_TIMEOUT=0
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "is still running; retry once it finishes" "$WORK/out"; then echo "took a failed Job for a running one" >&2; return 1; fi
  delete_line=$(grep -nxF "DELETE job strimzi-v1-api-conversion foreground" "$FAKE_CMDLOG" | head -1 | cut -d: -f1)
  crds_line=$(grep -nF "APPLY-CRDS" "$FAKE_CMDLOG" | cut -d: -f1)
  [ "$delete_line" -lt "$crds_line" ]
  grep -qF "APPLY-CONVERSION" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a conversion pod that cannot pull its image is named, with what to do" {
  prep
  export FAKE_JOB=hang FAKE_PULL=1 CONVERSION_TIMEOUT=0
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "cannot pull quay.io/strimzi/operator:0.51.0@sha256:" "$WORK/out"
  grep -qF "mirror" "$WORK/out"
  if grep -qE "PATCH|STAMP" "$FAKE_CMDLOG"; then echo "went on without a conversion" >&2; return 1; fi
  rm -rf "$WORK"
}

# A retry after the hook timed out must not touch the CRDs under a Job that
# is still converting.
@test "a Job of an earlier attempt still running is waited for before the CRDs are touched" {
  prep
  export FAKE_OLD_JOB=running
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF "still running" "$WORK/out"
  # It converted everything, so there is nothing left to apply or start.
  if grep -qE "APPLY-CRDS|APPLY-CONVERSION" "$FAKE_CMDLOG"; then echo "touched the CRDs under a running Job" >&2; return 1; fi
  grep -qxF "DELETE clusterrole strimzi-v1-api-conversion" "$FAKE_CMDLOG"
  grep -qxF "STAMP 61" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a Job of an earlier attempt still running after the wait aborts before the CRDs are touched" {
  prep
  export FAKE_OLD_JOB=running FAKE_JOB=hang CONVERSION_TIMEOUT=0
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "is still running; retry once it finishes" "$WORK/out"
  if grep -qE "SCALE-|APPLY-CRDS|DELETE|STAMP" "$FAKE_CMDLOG"; then echo "went on under a running Job" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failed read of the KafkaRebalances aborts before the operators are stopped" {
  prep
  export FAKE_REBALANCE_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "SCALE-|APPLY-CRDS|STAMP" "$FAKE_CMDLOG"; then echo "went on after a failed KafkaRebalance read" >&2; return 1; fi
  rm -rf "$WORK"
}

# Converting under a 0.45 operator the migration failed to see would let it
# record v1beta2 managedFields again after the rewrite.
@test "a failed read of the cluster operator Deployment aborts before anything is converted" {
  prep
  export FAKE_OPERATOR_READ_FAIL=1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qE "APPLY-CRDS|APPLY-CONVERSION|STAMP" "$FAKE_CMDLOG"; then echo "converted without knowing whether the 0.45 operator runs" >&2; return 1; fi
  rm -rf "$WORK"
}

# Run by hand after the kafka-operator release brought Strimzi 1.x: nothing
# would scale its operator back up.
@test "a re-run once the CRDs serve only v1 leaves the operators running" {
  prep
  printf '["v1"]' > "$WORK/state/stored"
  export FAKE_SERVED=v1
  rc=0
  run_migration 60 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qF "SCALE-" "$FAKE_CMDLOG"; then echo "stopped the 1.x operator" >&2; return 1; fi
  grep -qxF "STAMP 61" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
