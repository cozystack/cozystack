#!/bin/sh
# Restore a Talos cluster snapshot from a prebake archive into the e2e
# sandbox. End state mirrors a freshly-finished hack/e2e-prepare-cluster.bats
# run: 3 QEMU VMs booted, etcd healthy, k8s API at 192.168.123.10:6443,
# 3 nodes Ready, NO Cozystack installed yet.
#
# Caller must then run `make install-cozystack` from the PR's workspace
# to provision Cozystack on top — keeping the install path itself faithful
# to a fresh PR install instead of layering a diff onto baked-in state.
#
# Prerequisites:
#   - /workspace/prebake.tar.zst staged by the caller
#   - Bridge/tap config NOT yet applied (this script sets them up)
set -eux

cd /workspace

apt-get update -qq >/dev/null 2>&1 || true
apt-get install -y -q zstd >/dev/null 2>&1 || true

echo "::group::Extract prebake archive"
mkdir -p _restored
tar -I zstd -xf prebake.tar.zst -C _restored
ls -lh _restored/
echo "::endgroup::"

echo "::group::Restore credentials"
cp _restored/talosconfig talosconfig
cp _restored/kubeconfig kubeconfig
echo "::endgroup::"

echo "::group::Restore disk layout (system+seed+data per node)"
# QEMU drive order must match the fresh-install layout — system=vda,
# seed=vdb, data=vdc — because LINSTOR's storage pool (provisioned later
# by hack/e2e-post-install-prep.sh during Cozystack install) attaches
# against /dev/vdc explicitly.
for i in 1 2 3; do
  mkdir -p "srv${i}"
  mv "_restored/srv${i}-system.qcow2" "srv${i}/system.qcow2"
  mv "_restored/srv${i}-data.qcow2"   "srv${i}/data.qcow2"
  mv "_restored/srv${i}-seed.img"     "srv${i}/seed.img"
done
echo "::endgroup::"

echo "::group::Set up host networking (cozy-br0 + iptables + tap devs)"
# Mirrors the "Prepare networking and masquerading" + "Create tap devices"
# tests in hack/e2e-prepare-cluster.bats. Idempotent so it can recover
# from a partially-initialised sandbox.
ip link del cozy-br0 2>/dev/null || true
ip link add cozy-br0 type bridge
ip link set cozy-br0 up
ip address add 192.168.123.1/24 dev cozy-br0
iptables -t nat -D POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE 2>/dev/null || true
iptables -t nat -A POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE
for i in 1 2 3; do
  ip link del "cozy-srv${i}" 2>/dev/null || true
  ip tuntap add dev "cozy-srv${i}" mode tap
  ip link set "cozy-srv${i}" up
  ip link set "cozy-srv${i}" master cozy-br0
done
echo "::endgroup::"

echo "::group::Boot QEMU VMs from snapshot"
for i in 1 2 3; do
  qemu-system-x86_64 -machine type=pc,accel=kvm -cpu host -smp 8 -m 24576 \
    -device virtio-net,netdev=net0,mac="52:54:00:12:34:5${i}" \
    -netdev "tap,id=net0,ifname=cozy-srv${i},script=no,downscript=no" \
    -drive "file=srv${i}/system.qcow2,if=virtio,format=qcow2" \
    -drive "file=srv${i}/seed.img,if=virtio,format=raw" \
    -drive "file=srv${i}/data.qcow2,if=virtio,format=qcow2" \
    -display none -daemonize -pidfile "srv${i}/qemu.pid"
done
sleep 5
echo "::endgroup::"

echo "::group::Wait for Talos API + Kubernetes nodes Ready"
timeout 60 sh -ec 'until nc -nz 192.168.123.11 50000 && nc -nz 192.168.123.12 50000 && nc -nz 192.168.123.13 50000; do sleep 1; done'
timeout 180 sh -ec 'until [ $(kubectl get node --no-headers 2>/dev/null | grep -c " Ready ") -eq 3 ]; do sleep 5; kubectl get nodes 2>&1 | head -5; done'
echo "Cluster ready after restore"
kubectl get nodes
echo "::endgroup::"

echo "Prebake restore complete — caller should now run make install-cozystack"
