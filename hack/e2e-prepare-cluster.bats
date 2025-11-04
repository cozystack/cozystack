#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack end‑to‑end provisioning test (Bats)
# -----------------------------------------------------------------------------

@test "Required installer assets exist" {
  if [ ! -f _out/assets/nocloud-amd64.raw.xz ]; then
    echo "Missing: _out/assets/nocloud-amd64.raw.xz" >&2
    exit 1
  fi
}

@test "IPv4 forwarding is enabled" {
  if [ "$(cat /proc/sys/net/ipv4/ip_forward)" != 1 ]; then
    echo "IPv4 forwarding is disabled!" >&2
    echo >&2
    echo "Enable it with:" >&2
    echo "  echo 1 > /proc/sys/net/ipv4/ip_forward" >&2
    exit 1
  fi
}

@test "Clean previous VMs" {
 kill $(cat srv1/qemu.pid srv2/qemu.pid srv3/qemu.pid 2>/dev/null) 2>/dev/null || true
 rm -rf srv1 srv2 srv3
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

@test "Prepare cloud‑init drive for VMs" {
  mkdir -p srv1 srv2 srv3

  # Generate cloud‑init ISOs
  for i in 1 2 3; do
    echo "hostname: srv${i}" > "srv${i}/meta-data"

    cat > "srv${i}/user-data" <<'EOF'
#cloud-config
EOF

    cat > "srv${i}/network-config" <<EOF
version: 2
ethernets:
  eth0:
    dhcp4: false
    addresses:
      - "192.168.123.1${i}/26"
    gateway4: "192.168.123.1"
    nameservers:
      search: [cluster.local]
      addresses: [8.8.8.8]
EOF

    ( cd "srv${i}" && genisoimage \
        -output seed.img \
        -volid cidata -rational-rock -joliet \
        user-data meta-data network-config )
  done
}

@test "Use Talos NoCloud image from assets" {
  if [ ! -f _out/assets/nocloud-amd64.raw.xz ]; then
    echo "Missing _out/assets/nocloud-amd64.raw.xz" 2>&1
    exit 1
  fi

  rm -f nocloud-amd64.raw
  cp _out/assets/nocloud-amd64.raw.xz .
  xz --decompress nocloud-amd64.raw.xz
}

@test "Prepare VM disks" {
  for i in 1 2 3; do
    cp nocloud-amd64.raw srv${i}/system.img
    qemu-img resize srv${i}/system.img 50G
    qemu-img create srv${i}/data.img 200G
  done
}

@test "Create tap devices" {
  for i in 1 2 3; do
    ip link del cozy-srv${i} 2>/dev/null || true
    ip tuntap add dev cozy-srv${i} mode tap
    ip link set cozy-srv${i} up
    ip link set cozy-srv${i} master cozy-br0
  done
}

@test "Boot QEMU VMs" {
  for i in 1 2 3; do
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

@test "Wait until Talos API port 50000 is reachable on all machines" {
  timeout 60 sh -ec 'until nc -nz 192.168.123.11 50000 && nc -nz 192.168.123.12 50000 && nc -nz 192.168.123.13 50000; do sleep 1; done'
}

@test "Generate Talos cluster configuration" {
  # Cluster‑wide patches
  cat > patch.yaml <<'EOF'
machine:
  kubelet:
    nodeIP:
      validSubnets:
      - 192.168.123.0/24
    extraConfig:
      maxPods: 512
  kernel:
    modules:
    - name: openvswitch
    - name: drbd
      parameters:
        - usermode_helper=disabled
    - name: zfs
    - name: spl
  registries:
    mirrors:
      docker.io:
        endpoints:
        - https://dockerio.nexus.aenix.org
      cr.fluentbit.io:
        endpoints:
        - https://fluentbit.nexus.aenix.org
      docker-registry3.mariadb.com:
        endpoints:
        - https://mariadb.nexus.aenix.org
      gcr.io:
        endpoints:
        - https://gcr.nexus.aenix.org
      ghcr.io:
        endpoints:
        - https://ghcr.nexus.aenix.org
      quay.io:
        endpoints:
        - https://quay.nexus.aenix.org
      registry.k8s.io:
        endpoints:
        - https://k8s.nexus.aenix.org
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
    - 10.244.0.0/16
    serviceSubnets:
    - 10.96.0.0/16
EOF

  # Control‑plane‑only patches
  cat > patch-controlplane.yaml <<'EOF'
machine:
  nodeLabels:
    node.kubernetes.io/exclude-from-external-load-balancers:
      $patch: delete
  network:
    interfaces:
    - interface: eth0
      vip:
        ip: 192.168.123.10
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
  proxy:
    disabled: true
  discovery:
    enabled: false
  etcd:
    advertisedSubnets:
    - 192.168.123.0/24
EOF

  # Generate secrets once
  if [ ! -f secrets.yaml ]; then
    talosctl gen secrets
  fi

  rm -f controlplane.yaml worker.yaml talosconfig kubeconfig
  talosctl gen config --with-secrets secrets.yaml cozystack https://192.168.123.10:6443 \
           --config-patch=@patch.yaml --config-patch-control-plane @patch-controlplane.yaml
}

@test "Apply Talos configuration to the node" {
  # Apply the configuration to all three nodes
  for node in 11 12 13; do
    talosctl apply -f controlplane.yaml -n 192.168.123.${node} -e 192.168.123.${node} -i
  done

  # Wait for Talos services to come up again
  timeout 60 sh -ec 'until nc -nz 192.168.123.11 50000 && nc -nz 192.168.123.12 50000 && nc -nz 192.168.123.13 50000; do sleep 1; done'
}

@test "Bootstrap Talos cluster" {
  # Bootstrap etcd on the first node
  timeout 10 sh -ec 'until talosctl bootstrap -n 192.168.123.11 -e 192.168.123.11; do sleep 1; done'

  # Wait until etcd is healthy
  timeout 180 sh -ec 'until talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.10 >/dev/null 2>&1; do sleep 1; done'
  timeout 60 sh -ec 'while talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.10 2>&1 | grep -q "rpc error"; do sleep 1; done'

  # Retrieve kubeconfig
  rm -f kubeconfig
  talosctl kubeconfig kubeconfig -e 192.168.123.10 -n 192.168.123.10

  # Wait until all three nodes register in Kubernetes
  timeout 60 sh -ec 'until [ $(kubectl get node --no-headers | wc -l) -eq 3 ]; do sleep 1; done'
}
