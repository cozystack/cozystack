#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack end-to-end — restore a previously snapshotted cluster state
# -----------------------------------------------------------------------------
# Pulls the OCI artifact produced by e2e-save-state.bats, unpacks it (tar -Sx
# recreates the holes, so the disks stay sparse on disk), rebuilds the exact
# host networking the snapshot was taken with — same bridge, taps, IPs and MACs
# — boots the VMs straight from their raw disks and waits until the cluster has
# recovered from the power-off so e2e tests can run against it.
# -----------------------------------------------------------------------------

STATE_REF="${REGISTRY}/e2e-state:${STATE_TAG}"

@test "IPv4 forwarding is enabled" {
  if [ "$(cat /proc/sys/net/ipv4/ip_forward)" != 1 ]; then
    echo 1 > /proc/sys/net/ipv4/ip_forward
  fi
}

@test "Clean previous VMs" {
  kill $(cat srv1/qemu.pid srv2/qemu.pid srv3/qemu.pid 2>/dev/null) 2>/dev/null || true
  rm -rf srv1 srv2 srv3
}

@test "Pull and unpack cluster state from the registry" {
  set +x
  echo "$REGISTRY_PASSWORD" | oras login "${REGISTRY%%/*}" -u "$REGISTRY_USERNAME" --password-stdin
  set -x

  rm -f state.tar.zst
  oras pull "$STATE_REF"
  # -S recreates the holes, so the 50G/200G disks land as sparse files.
  tar -S -I 'zstd -T0' -xf state.tar.zst
  rm -f state.tar.zst
}

@test "Required disks were restored" {
  for i in 1 2 3; do
    if [ ! -f "srv${i}/system.img" ]; then
      echo "Missing: srv${i}/system.img" >&2
      exit 1
    fi
  done
}

@test "Prepare networking and masquerading" {
  ip link del cozy-br0 2>/dev/null || true
  ip link add cozy-br0 type bridge
  ip link set cozy-br0 up
  ip address add 192.168.123.1/24 dev cozy-br0

  # Masquerading rule – idempotent (delete first, then add)
  iptables -t nat -D POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE 2>/dev/null || true
  iptables -t nat -A POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE
}

@test "Create tap devices" {
  for i in 1 2 3; do
    ip link del cozy-srv${i} 2>/dev/null || true
    ip tuntap add dev cozy-srv${i} mode tap
    ip link set cozy-srv${i} up
    ip link set cozy-srv${i} master cozy-br0
  done
}

@test "Boot QEMU VMs from snapshot" {
  for i in 1 2 3; do
    # Keep the same drive order (system, seed, data), MACs and IPs as the
    # snapshot so the guests retain their disk identity and the VIP/endpoints
    # baked into talosconfig/kubeconfig stay valid.
    qemu-system-x86_64 -machine type=pc,accel=kvm -cpu host -smp 8 -m 24576 \
      -device virtio-net,netdev=net0,mac=52:54:00:12:34:5${i} \
      -netdev tap,id=net0,ifname=cozy-srv${i},script=no,downscript=no \
      -drive file=srv${i}/system.img,if=virtio,format=raw \
      -drive file=srv${i}/seed.img,if=virtio,format=raw \
      -drive file=srv${i}/data.img,if=virtio,format=raw \
      -display none -daemonize -pidfile srv${i}/qemu.pid
  done

  # Give qemu a few seconds to start up networking
  sleep 5
}

@test "Wait until Talos API port 50000 is reachable on all machines" {
  timeout 120 sh -ec 'until nc -nz 192.168.123.11 50000 && nc -nz 192.168.123.12 50000 && nc -nz 192.168.123.13 50000; do sleep 1; done'
}

@test "Wait until etcd is healthy" {
  # Endpoint a concrete node (not the VIP) so this does not block on VIP
  # re-election right after the power-on.
  if ! timeout 180 sh -ec 'until talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.11 >/dev/null 2>&1; do sleep 1; done'; then
    talosctl dmesg -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.11 || true
    exit 1
  fi
}

@test "Wait until all nodes are Ready" {
  timeout 300 sh -ec 'until kubectl wait --for=condition=Ready --all nodes --timeout=10s >/dev/null 2>&1; do sleep 2; done'
}

@test "Wait until all HelmReleases are ready again" {
  # The cluster comes back as if from a power cut; Flux reconciles every
  # HelmRelease back to ready before the platform is usable for tests.
  timeout 60 sh -ec 'until [ "$(kubectl get hr -A --no-headers 2>/dev/null | wc -l)" -gt 0 ]; do sleep 2; done'
  kubectl get hr -A --no-headers | awk '{print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' | sh -e
}
