#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 61 --> 62 (hand the snapshot CRDs over to
# the snapshot-controller release and replace the empty v1alpha1
# VolumeGroupSnapshot CRDs with v1beta2 ones). See
# packages/core/platform/images/migrations/migrations/61 for the mechanism.
#
# The fake keeps the CRDs in a state file and applies each annotate, label,
# delete and create to it, so a second run reads what the first one wrote. The
# v1beta2 CRDs the migration creates come from group-crds/, standing in for the
# copies `make image-migrations` bakes into the image.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-61-snapshot-crds.bats
# -----------------------------------------------------------------------------

load test_helper

FIXTURES="$PWD/hack/testdata/migration-61-snapshot-crds"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration61-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-61-$$"

cozy_cleanup() {
  rm -rf "$WORKROOT"
  return 0
}

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
    -e NAMESPACE="${NAMESPACE-}" \
    -e FAKE_HR_FAIL="${FAKE_HR_FAIL-}" \
    -e FAKE_CRD_FAIL="${FAKE_CRD_FAIL-}" \
    -e FAKE_DELETE_FAIL="${FAKE_DELETE_FAIL-}" \
    -e FAKE_RECORD_FAIL="${FAKE_RECORD_FAIL-}" \
    -e FAKE_CREATE_FAIL="${FAKE_CREATE_FAIL-}" \
    -e GROUP_CRD_DIR=/fakebin/group-crds \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 61 inside a jq-enabled" >&2
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
  cp "$FIXTURES/crds.json" "$WORK/state/"
  echo '{}' > "$WORK/state/objects.json"
  : > "$WORK/state/helmrelease"
  FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  unset FAKE_HR_FAIL FAKE_CRD_FAIL FAKE_DELETE_FAIL FAKE_RECORD_FAIL FAKE_CREATE_FAIL || true
  return 0
}

# edit_crds <jq filter> -- rewrite the CRD state before a run.
edit_crds() {
  jq "$1" "$WORK/state/crds.json" > "$WORK/state/crds.json.new"
  mv "$WORK/state/crds.json.new" "$WORK/state/crds.json"
  return 0
}

crd_field() {
  jq -r --arg n "$1" ".items[] | select(.metadata.name == \$n) | $2" "$WORK/state/crds.json"
}

@test "adopts the snapshot CRDs and replaces the empty v1alpha1 group snapshot CRDs" {
  prep
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  for p in volumesnapshotclasses volumesnapshotcontents volumesnapshots; do
    crd="$p.snapshot.storage.k8s.io"
    [ "$(crd_field "$crd" '.metadata.annotations["meta.helm.sh/release-name"]')" = "snapshot-controller" ]
    [ "$(crd_field "$crd" '.metadata.annotations["meta.helm.sh/release-namespace"]')" = "cozy-snapshot-controller" ]
    [ "$(crd_field "$crd" '.metadata.annotations["helm.sh/resource-policy"]')" = "keep" ]
    [ "$(crd_field "$crd" '.metadata.labels["app.kubernetes.io/managed-by"]')" = "Helm" ]
  done

  for p in volumegroupsnapshotclasses volumegroupsnapshotcontents volumegroupsnapshots; do
    crd="$p.groupsnapshot.storage.k8s.io"
    grep -qxF "DELETE $crd" "$FAKE_CMDLOG"
    grep -qxF "CREATE $crd" "$FAKE_CMDLOG"
    [ "$(crd_field "$crd" '[.spec.versions[] | select(.served) | .name] | index("v1beta2") != null')" = "true" ]
    [ "$(crd_field "$crd" '.metadata.annotations["meta.helm.sh/release-name"]')" = "snapshot-controller" ]
  done
  # Every delete comes before the first create, so a kept CRD is known before
  # anything is created.
  [ "$(grep -nE '^(DELETE|CREATE) ' "$FAKE_CMDLOG" | sed -n '3p' | cut -d' ' -f1 | cut -d: -f2)" = "DELETE" ]
  if grep -qF "ANNOTATE volumegroupsnapshot" "$FAKE_CMDLOG"; then echo "a v1alpha1 CRD was adopted" >&2; return 1; fi
  if grep -qF "RECORD" "$FAKE_CMDLOG"; then echo "unexpected record" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a second run finds nothing to change" {
  prep
  run_migration 61 >"$WORK/out" 2>&1
  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^(ANNOTATE|LABEL|DELETE|CREATE)" "$FAKE_CMDLOG"; then echo "second run changed a CRD" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a group snapshot CRD that holds objects is kept and recorded" {
  prep
  echo '{"volumegroupsnapshots.groupsnapshot.storage.k8s.io": 2}' > "$WORK/state/objects.json"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qxF "DELETE volumegroupsnapshots.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"; then
    echo "a CRD holding objects was deleted" >&2; return 1
  fi
  grep -qxF "DELETE volumegroupsnapshotcontents.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
  if grep -qF "CREATE" "$FAKE_CMDLOG"; then echo "a v1beta2 CRD was created next to a kept v1alpha1 one" >&2; return 1; fi
  grep -qF "cozystack.io/migration-61-needs-attention=volumegroupsnapshots.groupsnapshot.storage.k8s.io=2" "$FAKE_CMDLOG"
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failed record of the kept CRDs still stamps the version" {
  prep
  echo '{"volumegroupsnapshots.groupsnapshot.storage.k8s.io": 2}' > "$WORK/state/objects.json"
  export FAKE_RECORD_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  grep -qF "failed to record the CRDs needing attention" "$WORK/out"
  if grep -qF "RECORD" "$FAKE_CMDLOG"; then echo "the failed record was logged as written" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "CRDs the release did not install are left alone" {
  prep
  edit_crds '.items |= map(.metadata.labels = {})'
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^(ANNOTATE|LABEL|DELETE|CREATE)" "$FAKE_CMDLOG"; then echo "a foreign CRD was changed" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a group snapshot CRD that already serves v1beta2 is adopted, not deleted" {
  prep
  edit_crds '.items |= map(if .spec.group == "groupsnapshot.storage.k8s.io"
    then .spec.versions = [{name: "v1beta2", served: true, storage: true}]
      | .status.storedVersions = ["v1beta2"]
    else . end)'
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^(DELETE|CREATE)" "$FAKE_CMDLOG"; then echo "a v1beta2 CRD was replaced" >&2; return 1; fi
  [ "$(crd_field volumegroupsnapshots.groupsnapshot.storage.k8s.io '.metadata.annotations["meta.helm.sh/release-name"]')" = "snapshot-controller" ]
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a cluster without the snapshot-controller release only stamps the version" {
  prep
  rm "$WORK/state/helmrelease"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^(ANNOTATE|LABEL|DELETE|CREATE)" "$FAKE_CMDLOG"; then echo "unexpected change" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a cluster with the release but no group snapshot CRDs gets them created" {
  prep
  edit_crds '.items |= map(select(.spec.group != "groupsnapshot.storage.k8s.io"))'
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for p in volumegroupsnapshotclasses volumegroupsnapshotcontents volumegroupsnapshots; do
    grep -qxF "CREATE $p.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
  done
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing HelmRelease read aborts instead of treating the release as absent" {
  prep
  export FAKE_HR_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  grep -qF "$FAKE_HR_FAIL" "$WORK/out"
  if grep -qE "^(ANNOTATE|LABEL|DELETE|CREATE|STAMP)" "$FAKE_CMDLOG"; then echo "changed something after a failed HelmRelease read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing CRD read aborts instead of treating the CRD as absent" {
  prep
  export FAKE_CRD_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed CRD read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing delete aborts before stamping" {
  prep
  export FAKE_DELETE_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed delete" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing create aborts before stamping, and the retry creates the CRDs" {
  prep
  export FAKE_CREATE_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed create" >&2; return 1; fi

  unset FAKE_CREATE_FAIL
  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for p in volumegroupsnapshotclasses volumegroupsnapshotcontents volumegroupsnapshots; do
    grep -qxF "CREATE $p.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
  done
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "release labels on the group snapshot CRDs alone still get them recreated" {
  prep
  edit_crds '.items |= map(if .spec.group == "snapshot.storage.k8s.io" then .metadata.labels = {} else . end)'
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for p in volumegroupsnapshotclasses volumegroupsnapshotcontents volumegroupsnapshots; do
    grep -qxF "DELETE $p.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
    grep -qxF "CREATE $p.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
  done
  if grep -qF "ANNOTATE volumesnapshot" "$FAKE_CMDLOG"; then echo "a foreign snapshot CRD was adopted" >&2; return 1; fi
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "release labels on the group snapshot CRDs alone survive a failed create" {
  prep
  edit_crds '.items |= map(if .spec.group == "snapshot.storage.k8s.io" then .metadata.labels = {} else . end)'
  export FAKE_CREATE_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed create" >&2; return 1; fi

  unset FAKE_CREATE_FAIL
  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  for p in volumegroupsnapshotclasses volumegroupsnapshotcontents volumegroupsnapshots; do
    grep -qxF "CREATE $p.groupsnapshot.storage.k8s.io" "$FAKE_CMDLOG"
    [ "$(crd_field "$p.groupsnapshot.storage.k8s.io" '[.spec.versions[] | select(.served) | .name] | index("v1beta2") != null')" = "true" ]
  done
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a v1beta2 group snapshot CRD that still stores v1alpha1 is recorded, not adopted" {
  prep
  edit_crds '.items |= map(if .metadata.name == "volumegroupsnapshotcontents.groupsnapshot.storage.k8s.io"
    then .spec.versions = [{name: "v1alpha1", served: true, storage: false}, {name: "v1beta2", served: true, storage: true}]
      | .status.storedVersions = ["v1alpha1", "v1beta2"]
    else . end)'
  rc=0
  run_migration 61 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "^(ANNOTATE|LABEL|DELETE) volumegroupsnapshotcontents" "$FAKE_CMDLOG"; then
    echo "a CRD still storing v1alpha1 was adopted or deleted" >&2; return 1
  fi
  if grep -qF "CREATE" "$FAKE_CMDLOG"; then echo "a CRD was created next to the recorded one" >&2; return 1; fi
  grep -qF "cozystack.io/migration-61-needs-attention=volumegroupsnapshotcontents.groupsnapshot.storage.k8s.io=stored-v1alpha1" "$FAKE_CMDLOG"
  grep -qxF "STAMP 62" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}
