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

@test "container teardown reports compose failure after reclaiming host storage" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/zpools"
  calls="$tmp/calls"
  : >"$calls"
  for n in 1 2 3; do
    : >"$tmp/zpools/srv${n}.img"
  done

  printf '%s\n' \
    '#!/bin/sh' \
    'printf "docker:%s\n" "$*" >>"$CALLS"' \
    'exit 23' >"$tmp/bin/docker"
  printf '%s\n' \
    '#!/bin/sh' \
    'printf "zpool:%s\n" "$*" >>"$CALLS"' \
    'exit 0' >"$tmp/bin/zpool"
  printf '%s\n' \
    '#!/bin/sh' \
    'exec "$@"' >"$tmp/bin/sudo"
  chmod +x "$tmp/bin/docker" "$tmp/bin/zpool" "$tmp/bin/sudo"

  if PATH="$tmp/bin:$PATH" CALLS="$calls" make -s -C packages/core/testing \
    COMPOSE_PROJECT=cozy-cleanup-test \
    COMPOSE_FILE="$PWD/hack/e2e-compose.yaml" \
    ZPOOL_BACKING_DIR="$tmp/zpools" \
    delete-cluster-container >"$tmp/output" 2>&1; then
    echo "container teardown hid the compose failure" >&2
    return 1
  fi

  grep -Fq 'container compose teardown failed with exit 23' "$tmp/output"
  for n in 1 2 3; do
    grep -Fq "zpool:destroy data-srv${n}" "$calls"
    if [ -e "$tmp/zpools/srv${n}.img" ]; then
      echo "backing file srv${n}.img survived failed compose teardown" >&2
      return 1
    fi
  done
  if [ -d "$tmp/zpools" ]; then
    echo "empty backing directory survived failed compose teardown" >&2
    return 1
  fi
  rm -r "$tmp"
}

@test "privileged Talos container image is digest pinned" {
  image=$(yq '.["x-node"].image' hack/e2e-compose.yaml)
  if ! printf '%s\n' "$image" | grep -Eq '^ghcr\.io/siderolabs/talos:v1\.13\.5@sha256:[0-9a-f]{64}$'; then
    echo "mutable or unexpected Talos container image: $image" >&2
    return 1
  fi
}

@test "container startup does not mask stale compose teardown failure" {
  if grep -Eq 'docker compose .* down -v.*\|\| true' "$CONTAINER_UP"; then
    echo "container startup can proceed after stale compose teardown fails" >&2
    return 1
  fi
  grep -Fq 'die "failed to remove a stale ${COMPOSE_PROJECT} compose project before startup"' "$CONTAINER_UP"
}

@test "container teardown keeps backing file when zpool destroy fails" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/zpools"
  calls="$tmp/calls"
  : >"$calls"
  for n in 1 2 3; do
    : >"$tmp/zpools/srv${n}.img"
  done

  printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp/bin/docker"
  printf '%s\n' \
    '#!/bin/sh' \
    'printf "zpool:%s\n" "$*" >>"$CALLS"' \
    'if [ "$1" = destroy ] && [ "$2" = data-srv2 ]; then exit 29; fi' \
    'exit 0' >"$tmp/bin/zpool"
  printf '%s\n' '#!/bin/sh' 'exec "$@"' >"$tmp/bin/sudo"
  chmod +x "$tmp/bin/docker" "$tmp/bin/zpool" "$tmp/bin/sudo"

  if PATH="$tmp/bin:$PATH" CALLS="$calls" make -s -C packages/core/testing \
    COMPOSE_PROJECT=cozy-cleanup-test \
    COMPOSE_FILE="$PWD/hack/e2e-compose.yaml" \
    ZPOOL_BACKING_DIR="$tmp/zpools" \
    delete-cluster-container >"$tmp/output" 2>&1; then
    echo "container teardown hid the zpool destroy failure" >&2
    return 1
  fi

  grep -Fq 'failed to destroy zpool data-srv2 (exit 29)' "$tmp/output"
  grep -Fq 'keeping '"$tmp/zpools/srv2.img"' because data-srv2 is still imported' "$tmp/output"
  if [ ! -e "$tmp/zpools/srv2.img" ]; then
    echo "backing file was removed from the still-imported data-srv2 pool" >&2
    return 1
  fi
  for n in 1 3; do
    if [ -e "$tmp/zpools/srv${n}.img" ]; then
      echo "cleanup stopped before removing srv${n}.img" >&2
      return 1
    fi
  done
  rm -r "$tmp"
}

@test "container teardown keeps backing files when zpool inventory is unreadable" {
  tmp=$(mktemp -d)
  mkdir -p "$tmp/bin" "$tmp/zpools"
  calls="$tmp/calls"
  : >"$calls"
  for n in 1 2 3; do
    : >"$tmp/zpools/srv${n}.img"
  done

  printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp/bin/docker"
  printf '%s\n' \
    '#!/bin/sh' \
    'printf "zpool:%s\n" "$*" >>"$CALLS"' \
    'if [ "$1" = list ]; then exit 31; fi' \
    'exit 0' >"$tmp/bin/zpool"
  printf '%s\n' '#!/bin/sh' 'exec "$@"' >"$tmp/bin/sudo"
  chmod +x "$tmp/bin/docker" "$tmp/bin/zpool" "$tmp/bin/sudo"

  if PATH="$tmp/bin:$PATH" CALLS="$calls" make -s -C packages/core/testing \
    COMPOSE_PROJECT=cozy-cleanup-test \
    COMPOSE_FILE="$PWD/hack/e2e-compose.yaml" \
    ZPOOL_BACKING_DIR="$tmp/zpools" \
    delete-cluster-container >"$tmp/output" 2>&1; then
    echo "container teardown treated an unreadable zpool inventory as absence" >&2
    return 1
  fi

  grep -Fq 'failed to inventory zpools after data-srv1 lookup returned exit 31 (inventory exit 31)' "$tmp/output"
  if grep -Fq 'zpool:destroy ' "$calls"; then
    echo "container teardown destroyed a pool without a readable inventory" >&2
    return 1
  fi
  for n in 1 2 3; do
    if [ ! -e "$tmp/zpools/srv${n}.img" ]; then
      echo "backing file srv${n}.img was removed without proving its pool absent" >&2
      return 1
    fi
  done
  rm -r "$tmp"
}
