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
  if grep -qE '\$\{[A-Za-z_]+:[-=?+]' "$FIXTURE"; then echo "FAIL: the fixture must not carry a shell-style default envsubst leaves in place"; false; fi
}

@test "render-tenant expands every variable with the runner's defaults" {
  out="$(COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=teststore "$RUNNER" render-tenant)"
  if printf '%s' "$out" | grep -q '\${'; then echo "FAIL: the rendered tenant must not keep an unexpanded variable"; false; fi
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

# The tenant's super-admin kubeconfig is a credential for the whole tenant, kept
# under RUNNER_TEMP for one run. The workflow sets no path of its own, so these
# cases go through the same RUNNER_TEMP fallback the runner provides, with the
# override cleared.
KUBECONFIG_NAME=proxmox-e2e-tenant.kubeconfig
SUITE="$REPO_ROOT/hack/e2e-chainsaw/kubernetes-proxmox/chainsaw-test.yaml.disabled"

@test "tenant-node writes the tenant kubeconfig without group or other bits under umask 022" {
  work="$(mktemp -d)"
  stub="$(mktemp -d)"
  # The secret read answers a kubeconfig and the first tenant read fails, which
  # ends the check right after the fetch with the file in place.
  cat > "$stub/kubectl" <<'STUB'
#!/bin/sh
case "$*" in *"get secret"*) printf '%s' YXBpVmVyc2lvbjogdjEK; exit 0 ;; esac
exit 1
STUB
  chmod +x "$stub/kubectl"
  rc=0
  (umask 022; PATH="$stub:$PATH" RUNNER_TEMP="$work" COZY_PROXMOX_WORKDIR= COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s "$RUNNER" tenant-node) >/dev/null 2>&1 || rc=$?
  mode="$(ls -l "$work/$KUBECONFIG_NAME" | cut -c1-10)"
  rm -rf "$work" "$stub"
  [ "$rc" -ne 0 ]
  [ "$mode" = "-rw-------" ] || { echo "FAIL: the tenant kubeconfig was written $mode" >&2; exit 1; }
}

@test "apply-tenant removes a tenant kubeconfig an earlier run left behind" {
  work="$(mktemp -d)"
  stub="$(mktemp -d)"
  printf 'an earlier run\n' > "$work/$KUBECONFIG_NAME"
  printf '#!/bin/sh\ncat >/dev/null\n' > "$stub/kubectl"
  chmod +x "$stub/kubectl"
  PATH="$stub:$PATH" RUNNER_TEMP="$work" COZY_PROXMOX_WORKDIR= COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s "$RUNNER" apply-tenant
  left=no
  [ ! -e "$work/$KUBECONFIG_NAME" ] || left=yes
  rm -rf "$work" "$stub"
  [ "$left" = no ] || { echo "FAIL: apply-tenant left the earlier run's kubeconfig in place" >&2; exit 1; }
}

@test "the suite's finally removes the tenant kubeconfig, so diagnostics after it prints no tenant section" {
  # keep_tenant exits the finally before its deletes, and the kubeconfig has to
  # be gone on that path as well. Diagnostics reads whatever is at the path, so
  # a file left there would be reported as the next run's tenant.
  work="$(mktemp -d)"
  stub="$(mktemp -d)"
  printf 'this run\n' > "$work/$KUBECONFIG_NAME"
  printf '#!/bin/sh\necho "stub $*"\n' > "$stub/kubectl"
  printf '#!/bin/sh\nexit 0\n' > "$stub/ssh"
  chmod +x "$stub/kubectl" "$stub/ssh"
  finally="$(yq -r '.spec.steps[] | select(.name == "replacement-converges-unattended") | .finally[0].script.content' "$SUITE")"
  [ -n "$finally" ]
  PATH="$stub:$PATH" RUNNER_TEMP="$work" COZY_PROXMOX_WORKDIR= COZY_KEEP_TENANT=true sh -c "$finally" >/dev/null
  out="$(PATH="$stub:$PATH" RUNNER_TEMP="$work" COZY_PROXMOX_WORKDIR= COZY_PVE_SSH=x COZY_PROXMOX_STORAGE=s "$RUNNER" diagnostics 2>&1)"
  left=no
  [ ! -e "$work/$KUBECONFIG_NAME" ] || left=yes
  rm -rf "$work" "$stub"
  [ "$left" = no ] || { echo "FAIL: the suite's finally left the tenant kubeconfig in place" >&2; exit 1; }
  case "$out" in *"tenant nodes and pods"*) echo "$out" >&2; exit 1 ;; esac
}
