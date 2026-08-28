#!/usr/bin/env bats
# Unit coverage for the scheduler-capacity correction required by Talos
# container nodes. The live BATS suite checks the resulting Node status; this
# file pins the arithmetic and the cross-process plumbing without a cluster.

HACK_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")" && pwd)"
CONTAINER_UP="$HACK_DIR/e2e-container-up.sh"
E2E_CONTAINER_UP_LIB=true . "$CONTAINER_UP"

@test "container reservations reduce host capacity to one node limit" {
  reservations=$(calculate_node_reservations 32 131897116 8 24576 3)
  expected=$(printf '%s\n' 24000m 106731292Ki)

  if [ "$reservations" != "$expected" ]; then
    echo "unexpected kubelet reservations: $reservations" >&2
    return 1
  fi
}

@test "container reservations require aggregate host headroom" {
  if calculate_node_reservations 24 131897116 8 24576 3 >/dev/null 2>&1; then
    echo "three 8-CPU nodes unexpectedly fit on exactly 24 host CPUs" >&2
    return 1
  fi
  if calculate_node_reservations 32 75497472 8 24576 3 >/dev/null 2>&1; then
    echo "three 24-GiB nodes unexpectedly fit in exactly 72 GiB host memory" >&2
    return 1
  fi
}

@test "container storage pools match the QEMU lane capacity" {
  if ! grep -Fq 'ZPOOL_SIZE="${ZPOOL_SIZE:-200G}"' "$CONTAINER_UP"; then
    echo "container ZFS pools do not provide the QEMU lane's 200G per node" >&2
    return 1
  fi
}

@test "Kubernetes canonical memory units normalize to KiB" {
  for fixture in \
    '25165824Ki:25165824' \
    '24476Mi:25063424' \
    '23Gi:24117248'; do
    quantity=${fixture%%:*}
    expected=${fixture#*:}
    actual=$(kubernetes_memory_to_kib "$quantity")
    if [ "$actual" != "$expected" ]; then
      echo "$quantity normalized to $actual KiB, expected $expected KiB" >&2
      return 1
    fi
  done

  for invalid in 24476 24.0Gi garbage; do
    if kubernetes_memory_to_kib "$invalid" >/dev/null 2>&1; then
      echo "invalid quantity was accepted: $invalid" >&2
      return 1
    fi
  done
}

@test "container capacity values cross host compose sandbox and live assertion" {
  make_command=$(make -n -C packages/core/testing SANDBOX_NAME=test prepare-cluster-container)

  for expected in \
    'COZY_E2E_NODE_CPUS="8" COZY_E2E_NODE_MEMORY_MIB="24576"' \
    '-e COZY_E2E_NODE_CPUS="8" -e COZY_E2E_NODE_MEMORY_MIB="24576"'; do
    if ! printf '%s\n' "$make_command" | grep -Fq -- "$expected"; then
      echo "testing Makefile does not forward container capacity: $expected" >&2
      return 1
    fi
  done

  if ! grep -Fq 'cpus: ${COZY_E2E_NODE_CPUS:-8}' hack/e2e-compose.yaml \
    || ! grep -Fq 'mem_limit: ${COZY_E2E_NODE_MEMORY_MIB:-24576}m' hack/e2e-compose.yaml; then
    echo "compose limits are not sourced from the shared capacity values" >&2
    return 1
  fi
  if ! grep -Fq 'cpu: "${COZY_E2E_SYSTEM_RESERVED_CPU}"' "$CONTAINER_UP" \
    || ! grep -Fq 'memory: "${COZY_E2E_SYSTEM_RESERVED_MEMORY}"' "$CONTAINER_UP"; then
    echo "generated Talos config does not carry the calculated reservations" >&2
    return 1
  fi
  if ! grep -Fq '@test "Node allocatable matches the container limits"' hack/e2e-prepare-cluster-container.bats; then
    echo "container bring-up does not assert scheduler-visible capacity" >&2
    return 1
  fi
}

@test "container compose overrides reach both startup and teardown" {
  compose_file=/tmp/cozy-custom-compose.yaml
  make_command=$(make -n -C packages/core/testing \
    SANDBOX_NAME=test \
    COMPOSE_PROJECT=cozy-custom \
    COMPOSE_FILE="$compose_file" \
    ZPOOL_BACKING_DIR=/tmp/cozy-custom-zpools \
    prepare-cluster-container \
    delete-cluster-container)

  if ! printf '%s\n' "$make_command" | grep -Fq \
    "COMPOSE_PROJECT=\"cozy-custom\" COMPOSE_FILE=\"$compose_file\""; then
    echo "container startup does not receive compose overrides" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq \
    "docker compose -p \"cozy-custom\" -f \"$compose_file\" down -v"; then
    echo "container teardown does not reuse compose overrides" >&2
    return 1
  fi
  if ! printf '%s\n' "$make_command" | grep -Fq \
    'ZPOOL_BACKING_DIR="/tmp/cozy-custom-zpools"'; then
    echo "container startup does not receive the backing-directory override" >&2
    return 1
  fi
  for expected in \
    'zpool destroy "data-srv${n}"' \
    'rm -f "/tmp/cozy-custom-zpools/srv${n}.img"' \
    'rmdir "/tmp/cozy-custom-zpools"'; do
    if ! printf '%s\n' "$make_command" | grep -Fq "$expected"; then
      echo "container teardown does not remove its host storage: $expected" >&2
      return 1
    fi
  done
  cleanup_line=$(printf '%s\n' "$make_command" | grep -n 'zpool destroy' | cut -d: -f1)
  startup_line=$(printf '%s\n' "$make_command" | grep -n 'e2e-container-up.sh' | cut -d: -f1)
  if [ -z "$cleanup_line" ] || [ -z "$startup_line" ] || [ "$cleanup_line" -ge "$startup_line" ]; then
    echo "container storage cleanup does not run before startup" >&2
    return 1
  fi
}
