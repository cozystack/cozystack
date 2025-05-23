#!/usr/bin/env bats
# -----------------------------------------------------------------------------
# Cozystack end‑to‑end provisioning test (Bats)
# -----------------------------------------------------------------------------

export TALOSCONFIG=$PWD/talosconfig
export KUBECONFIG=$PWD/kubeconfig

@test "Environment variable COZYSTACK_INSTALLER_YAML is defined" {
  if [ -z "${COZYSTACK_INSTALLER_YAML:-}" ]; then
    echo 'COZYSTACK_INSTALLER_YAML environment variable is not set!' >&2
    echo >&2
    echo 'Please export it with the following command:' >&2
    echo '  export COZYSTACK_INSTALLER_YAML=$(helm template -n cozy-system installer packages/core/installer)' >&2
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

@test "Download Talos NoCloud image" {
  if [ ! -f nocloud-amd64.raw ]; then
    wget https://github.com/cozystack/cozystack/releases/latest/download/nocloud-amd64.raw.xz \
      -O nocloud-amd64.raw.xz --show-progress --output-file /dev/stdout --progress=dot:giga 2>/dev/null
    rm -f nocloud-amd64.raw
    xz --decompress nocloud-amd64.raw.xz
  fi
}

@test "Prepare VM disks" {
  for i in 1 2 3; do
    cp nocloud-amd64.raw srv${i}/system.img
    qemu-img resize srv${i}/system.img 20G
    qemu-img create srv${i}/data.img 100G
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
    qemu-system-x86_64 -machine type=pc,accel=kvm -cpu host -smp 8 -m 16384 \
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

@test "Install Cozystack" {
  # Create namespace & configmap required by installer
  kubectl create namespace cozy-system --dry-run=client -o yaml | kubectl apply -f -
  kubectl create configmap cozystack -n cozy-system \
          --from-literal=bundle-name=paas-full \
          --from-literal=ipv4-pod-cidr=10.244.0.0/16 \
          --from-literal=ipv4-pod-gateway=10.244.0.1 \
          --from-literal=ipv4-svc-cidr=10.96.0.0/16 \
          --from-literal=ipv4-join-cidr=100.64.0.0/16 \
          --from-literal=root-host=example.org \
          --from-literal=api-server-endpoint=https://192.168.123.10:6443 \
          --dry-run=client -o yaml | kubectl apply -f -

  # Apply installer manifests from env variable
  echo "$COZYSTACK_INSTALLER_YAML" | kubectl apply -f -

  # Wait for the installer deployment to become available
  kubectl wait deployment/cozystack -n cozy-system --timeout=1m --for=condition=Available

  # Wait until HelmReleases appear & reconcile them
  timeout 60 sh -ec 'until kubectl get hr -A | grep -q cozys; do sleep 1; done'
  sleep 5
  kubectl get hr -A | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n "$1" hr/"$2" &"} END {print "wait"}' | sh -ex

  # Fail the test if any HelmRelease is not Ready
  if kubectl get hr -A | grep -v " True " | grep -v NAME; then
    kubectl get hr -A
    fail "Some HelmReleases failed to reconcile"
  fi
}

@test "Wait for Cluster‑API provider deployments" {
  # Wait for Cluster‑API provider deployments
  timeout 60 sh -ec 'until kubectl get deploy -n cozy-cluster-api capi-controller-manager capi-kamaji-controller-manager capi-kubeadm-bootstrap-controller-manager capi-operator-cluster-api-operator capk-controller-manager >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deployment/capi-controller-manager deployment/capi-kamaji-controller-manager deployment/capi-kubeadm-bootstrap-controller-manager deployment/capi-operator-cluster-api-operator deployment/capk-controller-manager -n cozy-cluster-api --timeout=1m --for=condition=available
}

@test "Wait for LINSTOR and configure storage" {
  # Linstor controller and nodes
  kubectl wait deployment/linstor-controller -n cozy-linstor --timeout=5m --for=condition=available
  timeout 60 sh -ec 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) -eq 3 ]; do sleep 1; done'

  for node in srv1 srv2 srv3; do
    kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs ${node} /dev/vdc --pool-name data --storage-pool data
  done

  # Storage classes
  kubectl apply -f - <<'EOF'
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: local
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/layerList: "storage"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "false"
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: replicated
provisioner: linstor.csi.linbit.com
parameters:
  linstor.csi.linbit.com/storagePool: "data"
  linstor.csi.linbit.com/autoPlace: "3"
  linstor.csi.linbit.com/layerList: "drbd storage"
  linstor.csi.linbit.com/allowRemoteVolumeAccess: "true"
  property.linstor.csi.linbit.com/DrbdOptions/auto-quorum: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-no-data-accessible: suspend-io
  property.linstor.csi.linbit.com/DrbdOptions/Resource/on-suspended-primary-outdated: force-secondary
  property.linstor.csi.linbit.com/DrbdOptions/Net/rr-conflict: retry-connect
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
EOF
}

@test "Wait for MetalLB and configure address pool" {
  # MetalLB address pool
  kubectl apply -f - <<'EOF'
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  ipAddressPools: [cozystack]
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  addresses: [192.168.123.200-192.168.123.250]
  autoAssign: true
  avoidBuggyIPs: false
EOF
}

@test "Check Cozystack API service" {
  kubectl wait --for=condition=Available apiservices/v1alpha1.apps.cozystack.io --timeout=2m
}

@test "Configure Tenant and wait for applications" {
  # Patch root tenant and wait for its releases
  kubectl patch tenants/root -n tenant-root --type merge -p '{"spec":{"host":"example.org","ingress":true,"monitoring":true,"etcd":true,"isolated":true}}'

  timeout 60 sh -ec 'until kubectl get hr -n tenant-root etcd ingress monitoring tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/etcd hr/ingress hr/tenant-root -n tenant-root --timeout=2m --for=condition=ready

  if ! kubectl wait hr/monitoring -n tenant-root --timeout=2m --for=condition=ready; then
    flux reconcile hr monitoring -n tenant-root --force
    kubectl wait hr/monitoring -n tenant-root --timeout=2m --for=condition=ready
  fi

  # Expose Cozystack services through ingress
  kubectl patch configmap/cozystack -n cozy-system --type merge -p '{"data":{"expose-services":"api,dashboard,cdi-uploadproxy,vm-exportproxy,keycloak"}}'

  # NGINX ingress controller
  timeout 60 sh -ec 'until kubectl get deploy root-ingress-controller -n tenant-root >/dev/null 2>&1; do sleep 1; done'
  kubectl wait deploy/root-ingress-controller -n tenant-root --timeout=5m --for=condition=available

  # etcd statefulset
  kubectl wait sts/etcd -n tenant-root --for=jsonpath='{.status.readyReplicas}'=3 --timeout=5m

  # VictoriaMetrics components
  kubectl wait vmalert/vmalert-shortterm vmalertmanager/alertmanager -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m
  kubectl wait vlogs/generic -n tenant-root --for=jsonpath='{.status.updateStatus}'=operational --timeout=5m
  kubectl wait vmcluster/shortterm vmcluster/longterm -n tenant-root --for=jsonpath='{.status.clusterStatus}'=operational --timeout=5m

  # Grafana
  kubectl wait clusters.postgresql.cnpg.io/grafana-db -n tenant-root --for=condition=ready --timeout=5m
  kubectl wait deploy/grafana-deployment -n tenant-root --for=condition=available --timeout=5m

  # Verify Grafana via ingress
  ingress_ip=$(kubectl get svc root-ingress-controller -n tenant-root -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
  curl -sS -k "https://${ingress_ip}" -H 'Host: grafana.example.org' | grep -q Found
}

@test "Keycloak OIDC stack is healthy" {
  kubectl patch configmap/cozystack -n cozy-system --type merge -p '{"data":{"oidc-enabled":"true"}}'

  timeout 120 sh -ec 'until kubectl get hr -n cozy-keycloak keycloak keycloak-configure keycloak-operator >/dev/null 2>&1; do sleep 1; done'
  kubectl wait hr/keycloak hr/keycloak-configure hr/keycloak-operator -n cozy-keycloak --timeout=10m --for=condition=ready
}
