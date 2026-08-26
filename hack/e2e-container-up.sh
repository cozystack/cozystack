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
# 60 GB is NOT enough: LINSTOR can place both tenant workers on one node, and
# 2x20 GiB worker disks + 2x~21 GiB CDI import scratch + etcd is ~86 GiB. The
# QEMU lane gives each node a 200 GB /dev/vdc; this is the container equivalent.
ZPOOL_SIZE="${ZPOOL_SIZE:-120G}"
ZPOOL_BACKING_DIR="${ZPOOL_BACKING_DIR:-/var/lib/cozy-e2e-zpools}"
KUBERNETES_VERSION="${KUBERNETES_VERSION:-v1.33.12}"

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
docker exec "$SANDBOX_NAME" sh -ec '
cd /workspace
cat > container-patch.yaml <<PATCH
machine:
  kubelet:
    nodeIP:
      validSubnets:
      - 192.168.123.0/24
    extraConfig:
      maxPods: 512
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
