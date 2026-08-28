#!/bin/sh
# Bring up the container-lane substrate on the RUNNER HOST.
#
# The QEMU lane boots its guests inside the e2e sandbox. Containers cannot nest
# that way for free, so the Talos nodes are started as SIBLINGS on the runner's
# docker daemon and the sandbox is joined to their network. That keeps the host
# bind mounts in hack/e2e-compose.yaml resolving against the real host devtmpfs,
# which is the whole reason the lane can carry LINSTOR at all.
#
# Everything Talos-side (bootstrap, etcd, kubeconfig) stays in
# hack/e2e-prepare-cluster-container.bats, which runs inside the sandbox exactly
# as the QEMU lane's file does. This script owns only what must happen on the
# host and before the containers exist.
#
# POSIX sh with grep/find on purpose: this runs on CI runners and contributor
# machines, where rg/fd/bash-isms are not available.
set -eu

# kubernetes_memory_to_kib <quantity>
#
# Kubernetes canonicalizes binary memory quantities to the largest exact unit.
# A Node that previously reported Ki can therefore switch to Mi when kubelet
# reservations make the result an exact MiB value. Normalize the units used by
# Node status so the live capacity assertion validates the value, not its
# presentation.
kubernetes_memory_to_kib() {
  cozy_memory_quantity=$1
  case "$cozy_memory_quantity" in
    *Ki)
      cozy_memory_value=${cozy_memory_quantity%Ki}
      cozy_memory_multiplier=1
      ;;
    *Mi)
      cozy_memory_value=${cozy_memory_quantity%Mi}
      cozy_memory_multiplier=1024
      ;;
    *Gi)
      cozy_memory_value=${cozy_memory_quantity%Gi}
      cozy_memory_multiplier=$(( 1024 * 1024 ))
      ;;
    *)
      echo "unsupported Kubernetes memory quantity: '$cozy_memory_quantity'" >&2
      return 1
      ;;
  esac

  case "$cozy_memory_value" in
    '' | *[!0-9]*)
      echo "invalid Kubernetes memory quantity: '$cozy_memory_quantity'" >&2
      return 1
      ;;
  esac

  printf '%s\n' "$(( cozy_memory_value * cozy_memory_multiplier ))"
}

# calculate_node_reservations <host-cpus> <host-memory-kib>
#                             <node-cpus> <node-memory-mib> <node-count>
#
# Talos container nodes read capacity from the shared host kernel, not from
# their Docker CPU/memory cgroups. Return the kubelet systemReserved quantities
# that reduce each node's scheduler-visible capacity to its container limit.
# Also reject a host that cannot hold all node limits with strict headroom,
# matching the QEMU lane's runner contract instead of relying on cgroup OOMs.
calculate_node_reservations() {
  cozy_host_cpus=$1
  cozy_host_memory_kib=$2
  cozy_node_cpus=$3
  cozy_node_memory_mib=$4
  cozy_node_count=$5

  for cozy_quantity in "$cozy_host_cpus" "$cozy_host_memory_kib" "$cozy_node_cpus" "$cozy_node_memory_mib" "$cozy_node_count"; do
    case "$cozy_quantity" in
      '' | *[!0-9]* | 0)
        echo "all container capacity inputs must be positive integers, got '$cozy_quantity'" >&2
        return 1
        ;;
    esac
  done

  cozy_required_cpus=$(( cozy_node_cpus * cozy_node_count ))
  cozy_required_memory_kib=$(( cozy_node_memory_mib * 1024 * cozy_node_count ))
  if [ "$cozy_host_cpus" -le "$cozy_required_cpus" ]; then
    echo "container lane needs more than ${cozy_required_cpus} host CPUs, found ${cozy_host_cpus}" >&2
    return 1
  fi
  if [ "$cozy_host_memory_kib" -le "$cozy_required_memory_kib" ]; then
    echo "container lane needs more than ${cozy_required_memory_kib} KiB host memory, found ${cozy_host_memory_kib} KiB" >&2
    return 1
  fi

  printf '%sm\n' "$(( (cozy_host_cpus - cozy_node_cpus) * 1000 ))"
  printf '%sKi\n' "$(( cozy_host_memory_kib - cozy_node_memory_mib * 1024 ))"
}

# Unit tests and the live capacity assertion source only the helpers above.
if [ "${E2E_CONTAINER_UP_LIB:-false}" = true ]; then
  return 0 2>/dev/null || exit 0
fi

SANDBOX_NAME="${SANDBOX_NAME:?SANDBOX_NAME must be set (packages/core/testing/Makefile sets it)}"
# Resolved from this script's own location, not the caller's cwd: the Makefile
# target runs it from packages/core/testing, where a relative hack/ does not
# exist, while a contributor runs it from the repo root, where it does. Deriving
# the root here makes both work and keeps the compose project name, the file and
# the teardown in packages/core/testing/Makefile referring to the same thing.
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)
COMPOSE_FILE="${COMPOSE_FILE:-${REPO_ROOT}/hack/e2e-compose.yaml}"
COMPOSE_PROJECT="${COMPOSE_PROJECT:-cozy-e2e}"
# A smaller pool is not safe even when its sparse backing file fits physically:
# root-tenant local PVCs can leave a satellite with less than the ~21 GiB needed
# by CDI scratch. Once a worker's 20 GiB target PV is bound there, node affinity
# forces the importer and scratch volume onto that same full satellite. Match the
# QEMU lane's per-node 200 GB /dev/vdc so placement cannot turn node join into a
# storage-capacity lottery.
ZPOOL_SIZE="${ZPOOL_SIZE:-200G}"
ZPOOL_BACKING_DIR="${ZPOOL_BACKING_DIR:-/var/lib/cozy-e2e-zpools}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.33.12}"
COZY_E2E_NODE_CPUS="${COZY_E2E_NODE_CPUS:-8}"
COZY_E2E_NODE_MEMORY_MIB="${COZY_E2E_NODE_MEMORY_MIB:-24576}"
export COZY_E2E_NODE_CPUS COZY_E2E_NODE_MEMORY_MIB

log() { echo "[e2e-container-up] $*"; }
die() { echo "[e2e-container-up] FATAL: $*" >&2; exit 1; }

# modprobe and zpool need root; docker does not, and must NOT be run through
# sudo, or the compose project ends up owned by a different user than the
# teardown in packages/core/testing/Makefile runs as. So escalate per command
# rather than re-exec'ing the whole script. Empty when already root, which is
# how this runs on a dev box; `sudo` on a CI runner, which runs as an
# unprivileged user with passwordless sudo.
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  command -v sudo >/dev/null 2>&1 \
    || die "not root and no sudo available; this script needs to load kernel
     modules and create zpools on the host."
  SUDO="sudo"
fi

HOST_CPUS=$(getconf _NPROCESSORS_ONLN)
HOST_MEMORY_KIB=$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)
NODE_RESERVATIONS=$(calculate_node_reservations \
  "$HOST_CPUS" "$HOST_MEMORY_KIB" \
  "$COZY_E2E_NODE_CPUS" "$COZY_E2E_NODE_MEMORY_MIB" 3) \
  || die "the runner cannot safely host three configured Talos containers"
SYSTEM_RESERVED_CPU=$(printf '%s\n' "$NODE_RESERVATIONS" | sed -n '1p')
SYSTEM_RESERVED_MEMORY=$(printf '%s\n' "$NODE_RESERVATIONS" | sed -n '2p')
log "node limit ${COZY_E2E_NODE_CPUS} CPU/${COZY_E2E_NODE_MEMORY_MIB} MiB; kubelet systemReserved ${SYSTEM_RESERVED_CPU}/${SYSTEM_RESERVED_MEMORY} against host ${HOST_CPUS} CPU/${HOST_MEMORY_KIB} KiB"

# ---------------------------------------------------------------------------
# 1. Host kernel modules.
#
# machine.kernel.modules is a SILENT no-op in container mode --
# internal/app/machined/pkg/controllers/runtime/kernel_module_spec.go returns
# early on ModeContainer with no error and no event -- so a module the QEMU lane
# declares in its machine config simply never loads here. Asserting it now makes
# absence fail at provisioning instead of surfacing an hour later as a storage
# or CNI error with no obvious cause.
# ---------------------------------------------------------------------------
for mod in openvswitch zfs; do
  $SUDO modprobe "$mod" 2>/dev/null || true
  if ! grep -q "^${mod} " /proc/modules; then
    die "kernel module '${mod}' is not loaded on the host.
     The container lane takes its kernel from the host and Talos cannot load
     modules in container mode, so this must be loaded before the nodes start.
     openvswitch ships with the distro kernel, so 'modprobe openvswitch' is
     usually enough; zfs needs the OpenZFS packages first (on Ubuntu,
     'apt-get install zfsutils-linux', then 'modprobe zfs')."
  fi
  log "host module ${mod}: loaded"
done

# ---------------------------------------------------------------------------
# 2. Per-node ZFS pools.
#
# zpools are GLOBAL PER KERNEL, so every node sees every pool. They must be
# node-distinct and each satellite pinned to its own; nothing enforces the
# pinning. Acceptable for CI, not a production pattern.
# ---------------------------------------------------------------------------
$SUDO mkdir -p "$ZPOOL_BACKING_DIR"
for n in 1 2 3; do
  if $SUDO zpool list "data-srv${n}" >/dev/null 2>&1; then
    log "zpool data-srv${n}: already present"
    continue
  fi
  $SUDO truncate -s "$ZPOOL_SIZE" "${ZPOOL_BACKING_DIR}/srv${n}.img"
  $SUDO zpool create -f "data-srv${n}" "${ZPOOL_BACKING_DIR}/srv${n}.img"
  log "zpool data-srv${n}: created (${ZPOOL_SIZE})"
done

# ---------------------------------------------------------------------------
# 3. Machine config, generated inside the sandbox so it uses the SAME pinned
#    talosctl the QEMU lane does.
#
# Differences from the QEMU lane's patches, each forced by container mode:
#   * no machine.kernel.modules  -- silent no-op, see above
#   * no /etc/lvm/lvm.conf filter -- no LVM in play
#   * no ARP VIP                 -- unavailable in container mode, so the
#                                   apiServerEndpoint is a node IP
# Everything else is deliberately identical.
# ---------------------------------------------------------------------------
docker exec \
  -e COZY_E2E_SYSTEM_RESERVED_CPU="$SYSTEM_RESERVED_CPU" \
  -e COZY_E2E_SYSTEM_RESERVED_MEMORY="$SYSTEM_RESERVED_MEMORY" \
  "$SANDBOX_NAME" sh -ec '
cd /workspace
cat > container-patch.yaml <<PATCH
machine:
  kubelet:
    nodeIP:
      validSubnets:
      - 192.168.123.0/24
    extraConfig:
      maxPods: 512
      systemReserved:
        cpu: "${COZY_E2E_SYSTEM_RESERVED_CPU}"
        memory: "${COZY_E2E_SYSTEM_RESERVED_MEMORY}"
  registries:
    mirrors:
      docker.io:
        endpoints:
        - https://mirror.gcr.io
  files:
  - content: |
      [plugins]
        [plugins."io.containerd.cri.v1.runtime"]
          device_ownership_from_security_context = true
    path: /etc/cri/conf.d/20-customization.part
    op: create
cluster:
  apiServer:
    extraArgs:
      oidc-issuer-url: "https://keycloak.example.org/realms/cozy"
      oidc-client-id: "kubernetes"
      oidc-username-claim: "preferred_username"
      oidc-groups-claim: "groups"
  network:
    cni:
      name: none
    dnsDomain: cozy.local
    podSubnets:
    - 100.65.0.0/16
    serviceSubnets:
    - 10.96.0.0/16
PATCH
cat > container-patch-controlplane.yaml <<PATCH
machine:
  nodeLabels:
    node.kubernetes.io/exclude-from-external-load-balancers:
      \$patch: delete
cluster:
  allowSchedulingOnControlPlanes: true
  controllerManager:
    extraArgs:
      bind-address: 0.0.0.0
  scheduler:
    extraArgs:
      bind-address: 0.0.0.0
  apiServer:
    certSANs:
    - 127.0.0.1
    - 192.168.123.11
    - 192.168.123.12
    - 192.168.123.13
  proxy:
    disabled: true
  discovery:
    enabled: false
  etcd:
    advertisedSubnets:
    - 192.168.123.0/24
PATCH
[ -f secrets.yaml ] || talosctl gen secrets
rm -f controlplane.yaml worker.yaml talosconfig kubeconfig
talosctl gen config --with-secrets secrets.yaml cozystack https://192.168.123.11:6443 \
  --kubernetes-version '"$KUBERNETES_VERSION"' \
  --output-types controlplane,talosconfig -o . --force \
  --config-patch @container-patch.yaml \
  --config-patch-control-plane @container-patch-controlplane.yaml
'
log "machine config generated (kubernetes ${KUBERNETES_VERSION})"

USERDATA=$(docker exec "$SANDBOX_NAME" base64 -w0 /workspace/controlplane.yaml)
export USERDATA
[ -n "$USERDATA" ] || die "USERDATA is empty; talosctl gen config produced nothing"

# ---------------------------------------------------------------------------
# 4. Start the nodes and put the sandbox on their network.
# ---------------------------------------------------------------------------
docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" down -v >/dev/null 2>&1 || true
docker compose -p "$COMPOSE_PROJECT" -f "$COMPOSE_FILE" up -d
log "three Talos containers started"

NETWORK="${COMPOSE_PROJECT}_talos"
if docker network inspect "$NETWORK" -f '{{range $k,$v := .Containers}}{{$v.Name}} {{end}}' | grep -q "\b${SANDBOX_NAME}\b"; then
  log "sandbox already attached to ${NETWORK}"
else
  docker network connect "$NETWORK" "$SANDBOX_NAME"
  log "sandbox attached to ${NETWORK}"
fi

log "substrate up; hack/e2e-prepare-cluster-container.bats takes it from here"
