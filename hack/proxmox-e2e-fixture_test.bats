#!/usr/bin/env bats
# The Proxmox e2e fixture is stand-agnostic through ${COZY_PVE_*} variables that
# hack/e2e-chainsaw/_lib/run-proxmox.sh expands with envsubst. GNU envsubst
# expands ${VAR} and nothing else: a ${VAR:-default} stays in the text as it is,
# set or not, and reaches the apiserver as a string where the schema wants an
# integer. So the defaults live in the script, the fixture carries bare
# variables, and the render refuses to hand out a fixture with one left in it.

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
