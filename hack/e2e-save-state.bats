#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack end-to-end — snapshot a consistent cluster state
# -----------------------------------------------------------------------------
# Runs after install-cozystack, before any e2e app test. Force-powers-off all
# QEMU VMs and streams a sparse-aware, zstd-compressed tarball of their disks
# plus the cluster access files straight into the registry as an OCI artifact —
# no qemu-img conversion and no docker cp, everything happens inside the
# sandbox.
#
# Talos refuses a simultaneous graceful shutdown of every node, so we hard
# power-off instead: SIGTERM makes QEMU stop the guest and flush its block
# caches to the backing image before exiting. The result is crash-consistent at
# the block level — etcd/LINSTOR recover on the next boot exactly as they would
# after a power cut, which is all the e2e cluster needs.
#
# tar -S records the holes (GNU sparse format) and zstd compresses fast; the
# restore side recreates the holes with tar -Sx, so the 50G/200G disks travel
# and land as their few GB of actually-written data.
# -----------------------------------------------------------------------------

STATE_REF="${REGISTRY}/e2e-state:${STATE_TAG}"

@test "Force power-off all VMs" {
  pids=
  for i in 1 2 3; do
    if [ -f "srv${i}/qemu.pid" ]; then
      pids="$pids $(cat srv${i}/qemu.pid)"
    fi
  done

  # SIGTERM = orderly QEMU shutdown: the guest is hard powered-off while QEMU
  # flushes the backing image, leaving a consistent file on the host.
  for pid in $pids; do
    kill "$pid" 2>/dev/null || true
  done

  # Wait until every QEMU process is actually gone (and has finished flushing)
  # before reading the images, otherwise tar would race a live writer.
  for pid in $pids; do
    timeout 60 sh -ec "while kill -0 $pid 2>/dev/null; do sleep 1; done"
  done
}

@test "Pack and push cluster state to the registry" {
  # Keep the token out of the trace; GitHub also masks it, but belt and braces.
  set +x
  echo "$REGISTRY_PASSWORD" | oras login "${REGISTRY%%/*}" -u "$REGISTRY_USERNAME" --password-stdin
  set -x

  rm -f state.tar.zst
  # -S: store holes efficiently. -I 'zstd -T0': multithreaded zstd (fast).
  # The cluster access files (talosconfig/kubeconfig/...) ride along — without
  # them the restored cluster is unreachable.
  tar -S -I 'zstd -T0' -cf state.tar.zst \
    srv1 srv2 srv3 \
    talosconfig kubeconfig secrets.yaml \
    controlplane.yaml worker.yaml patch.yaml patch-controlplane.yaml

  oras push "$STATE_REF" state.tar.zst:application/zstd

  rm -f state.tar.zst
}
