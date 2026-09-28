#!/usr/bin/env bats
# The Proxmox e2e fixture is stand-agnostic through ${COZY_PVE_*} variables that
# hack/e2e-chainsaw/_lib/run-proxmox.sh expands with envsubst. GNU envsubst
# expands ${VAR} and nothing else: a ${VAR:-default} stays in the text as it is,
# set or not, and reaches the apiserver as a string where the schema wants an
# integer. So the defaults live in the script, the fixture carries bare
# variables, and the render refuses to hand out a fixture with one left in it.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
FIXTURE="$REPO_ROOT/hack/e2e-chainsaw/kubernetes-proxmox/tenant.yaml"
RUNNER="$REPO_ROOT/hack/e2e-chainsaw/_lib/run-proxmox.sh"

@test "the fixture carries no shell-style default that envsubst would leave in place" {
  ! grep -qE '\$\{[A-Za-z_]+:[-=?+]' "$FIXTURE"
}

@test "render-tenant expands every variable with the runner's defaults" {
  out="$(COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=teststore "$RUNNER" render-tenant)"
  ! printf '%s' "$out" | grep -q '\${'
  printf '%s' "$out" | grep -qE '^ +prefix: 24$'
  printf '%s' "$out" | grep -qE '^ +storage: "teststore"$'
  printf '%s' "$out" | grep -qE '^ +bridge: "vmbr50"$'
}

@test "render-tenant honours a stand's own values and keeps prefix an integer" {
  out="$(COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s COZY_PVE_PREFIX=16 COZY_PVE_BRIDGE=vmbr1 "$RUNNER" render-tenant)"
  printf '%s' "$out" | grep -qE '^ +prefix: 16$'
  printf '%s' "$out" | grep -qE '^ +bridge: "vmbr1"$'
}

@test "render-tenant passes the stand's resource pool through, and none by default" {
  out="$(COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s COZY_PVE_POOL=tenant-e2e "$RUNNER" render-tenant)"
  printf '%s' "$out" | grep -qE '^ +pool: "tenant-e2e"$'
  out="$(COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s "$RUNNER" render-tenant)"
  printf '%s' "$out" | grep -qE '^ +pool: ""$'
}

# The PVC probe waits for the disk to leave the storage. A listing that fails
# must fail the check: read as empty, it would report a leaked disk as freed.
# (The bodies stay POSIX sh, so the legacy hack/cozytest.sh translator can still
# run them: no `run`, no [[ ]].)
@test "disk-freed fails when the storage cannot be listed, instead of calling the disk gone" {
  stub="$(mktemp -d)"
  printf '#!/bin/sh\necho "ssh: connect to host pve: Connection refused" >&2\nexit 255\n' > "$stub/ssh"
  chmod +x "$stub/ssh"
  rc=0
  out="$(PATH="$stub:$PATH" COZY_PVE_SSH=root@pve COZY_PROXMOX_STORAGE=main-pool "$RUNNER" disk-freed main-pool:vm-1-pvc-x 2>&1)" || rc=$?
  rm -rf "$stub"
  [ "$rc" -ne 0 ]
  case "$out" in *"cannot list main-pool"*) ;; *) echo "$out" >&2; exit 1 ;; esac
  case "$out" in *"is gone"*) echo "$out" >&2; exit 1 ;; esac
}

@test "disk-freed reports the disk gone once a successful listing no longer shows it" {
  stub="$(mktemp -d)"
  printf '#!/bin/sh\necho "Volid Format Type Size VMID"\necho "main-pool:vm-2-pvc-y raw images 1073741824 2"\n' > "$stub/ssh"
  chmod +x "$stub/ssh"
  out="$(PATH="$stub:$PATH" COZY_PVE_SSH=root@pve COZY_PROXMOX_STORAGE=main-pool "$RUNNER" disk-freed main-pool:vm-1-pvc-x 2>&1)"
  rm -rf "$stub"
  case "$out" in *"is gone from main-pool"*) ;; *) echo "$out" >&2; exit 1 ;; esac
}
