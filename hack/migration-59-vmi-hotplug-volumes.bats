#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Unit tests for platform migration 59 --> 60 (copy VMI-only hotplugged volumes
# into the VM spec). See packages/core/platform/images/migrations/migrations/59
# for the mechanism.
#
# The fake keeps the VMs in a state file and applies each JSON patch to it, so
# a second run reads what the first one wrote.
#
# The harness (docker, jq baked onto the migrations base image, the explicit
# `return` in run_migration) is the one hack/migration-55-fluxcd-orphan.bats
# describes; see there for why each piece is load-bearing.
#
# Run with: hack/cozytest.sh hack/migration-59-vmi-hotplug-volumes.bats
# -----------------------------------------------------------------------------

FIXTURES="$PWD/hack/testdata/migration-59-vmi-hotplug"
MIG_DIR="$PWD/packages/core/platform/images/migrations/migrations"
ALPINE=$(sed -n 's/^FROM \(alpine:[^ ]*\).*$/\1/p' \
  "$PWD/packages/core/platform/images/migrations/Dockerfile" | head -1)
TESTIMG="cozystack-migration59-test:$(printf '%s' "$ALPINE" | sed 's/[^a-zA-Z0-9]/-/g')"
WORKROOT="${TMPDIR:-/tmp}/cozy-migration-59-$$"

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
    -e FAKE_NO_KUBEVIRT="${FAKE_NO_KUBEVIRT-}" \
    -e FAKE_CRD_FAIL="${FAKE_CRD_FAIL-}" \
    -e FAKE_LIST_FAIL="${FAKE_LIST_FAIL-}" \
    -e FAKE_VMI_LIST_FAIL="${FAKE_VMI_LIST_FAIL-}" \
    -e FAKE_PATCH_FAIL="${FAKE_PATCH_FAIL-}" \
    "$TESTIMG" "/migrations/$1" || _run_migration_rc=$?
  return "$_run_migration_rc"
}

prep() {
  docker info >/dev/null 2>&1 || {
    echo "docker is required: these tests run migration 59 inside a jq-enabled" >&2
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
  cp "$FIXTURES/vms.json" "$FIXTURES/vmis.json" "$WORK/state/"
  FAKE_CMDLOG="$WORK/cmdlog"
  : > "$FAKE_CMDLOG"
  export NAMESPACE=cozy-system
  unset FAKE_NO_KUBEVIRT FAKE_CRD_FAIL FAKE_LIST_FAIL FAKE_VMI_LIST_FAIL FAKE_PATCH_FAIL || true
  return 0
}

vm_field() {
  jq -r --arg ns "$1" --arg n "$2" \
    ".items[] | select(.metadata.namespace == \$ns and .metadata.name == \$n) | $3" \
    "$WORK/state/vms.json"
}

@test "copies VMI-only hotplugged volumes and their disks into the VM that needs them" {
  prep
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]

  # Both hotpluggable kinds are copied, the non-hotpluggable VMI volume is not,
  # and the copied volume keeps the source it has on the VMI.
  grep -qxF "PATCH-VM tenant-a/worker-old volumes=root,pvc-in-spec,pvc-vmi-only,pvc-claim-only disks=root,pvc-in-spec,pvc-vmi-only,pvc-claim-only" "$FAKE_CMDLOG"
  [ "$(vm_field tenant-a worker-old '.spec.template.spec.volumes[] | select(.name == "pvc-claim-only") | .persistentVolumeClaim.hotpluggable')" = "true" ]
  [ "$(vm_field tenant-a worker-old '.spec.template.spec.domain.devices.disks[] | select(.name == "pvc-vmi-only") | .serial')" = "vmi-only" ]

  # A disk the VM already names is not added a second time.
  grep -qxF "PATCH-VM tenant-d/worker-disk volumes=root,pvc-d disks=root,pvc-d" "$FAKE_CMDLOG"

  # A VM whose spec already lists every hotplug volume is left alone, and so is
  # a VMI no VM controls: an orphan, one whose VM reference is not the
  # controller, or one whose controlling VM is already gone. VM-owned means a
  # controller reference here, as in virt-api. A VMI that is not Running, or
  # reports no phase yet, is left alone too: virt-controller reconciles only a
  # running VMI, and the next start builds the VMI from the VM spec.
  if grep -qE "^PATCH-VM tenant-(b|c|e|f|g|h)/" "$FAKE_CMDLOG"; then echo "unexpected patch" >&2; return 1; fi
  grep -qF "tenant-a/worker-old: adding hotplugged volumes pvc-vmi-only,pvc-claim-only" "$WORK/out"
  grep -qxF "STAMP 60" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a second run finds nothing to change" {
  prep
  run_migration 59 >"$WORK/out" 2>&1
  : > "$FAKE_CMDLOG"
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qF "PATCH-VM" "$FAKE_CMDLOG"; then echo "second run patched a VM" >&2; return 1; fi
  grep -qF "nothing to update" "$WORK/out"
  grep -qxF "STAMP 60" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a cluster without KubeVirt only stamps the version" {
  prep
  export FAKE_NO_KUBEVIRT=1
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -eq 0 ]
  if grep -qE "get virtualmachine|PATCH-VM" "$FAKE_CMDLOG"; then echo "unexpected VM access" >&2; return 1; fi
  grep -qxF "STAMP 60" "$FAKE_CMDLOG"
  rm -rf "$WORK"
}

@test "a failing VM list aborts before stamping" {
  prep
  export FAKE_LIST_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed list" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing CRD read aborts instead of treating KubeVirt as absent" {
  prep
  export FAKE_CRD_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed CRD read" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing VMI list aborts before stamping" {
  prep
  export FAKE_VMI_LIST_FAIL="Error from server (InternalError): etcdserver: request timed out"
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed VMI list" >&2; return 1; fi
  rm -rf "$WORK"
}

@test "a failing patch aborts before stamping" {
  prep
  export FAKE_PATCH_FAIL="Error from server (Conflict): the object has been modified"
  rc=0
  run_migration 59 >"$WORK/out" 2>&1 || rc=$?
  cat "$WORK/out"; cat "$FAKE_CMDLOG"
  [ "$rc" -ne 0 ]
  if grep -qF "STAMP" "$FAKE_CMDLOG"; then echo "unexpected STAMP after failed patch" >&2; return 1; fi
  rm -rf "$WORK"
}
