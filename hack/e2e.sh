#!/bin/bash
if [ "$COZYSTACK_INSTALLER_YAML" = "" ]; then
  echo 'COZYSTACK_INSTALLER_YAML variable is not set!' >&2
  echo 'please set it with following command:' >&2
  echo >&2
  echo 'export COZYSTACK_INSTALLER_YAML=$(helm template -n cozy-system installer packages/core/installer)' >&2
  echo >&2
  exit 1
fi

if [ "$(cat /proc/sys/net/ipv4/ip_forward)" != 1 ]; then
  echo "IPv4 forwarding is not enabled!" >&2
  echo 'please enable forwarding with the following command:' >&2
  echo >&2
  echo 'echo 1 > /proc/sys/net/ipv4/ip_forward' >&2
  echo >&2
  exit 1
fi

set -x
set -e

kill `cat srv1/qemu.pid srv2/qemu.pid srv3/qemu.pid` || true

ip link del cozy-br0 || true
ip link add cozy-br0 type bridge
ip link set cozy-br0 up
ip addr add 192.168.123.1/24 dev cozy-br0

# Enable masquerading
iptables -t nat -D POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE 2>/dev/null || true
iptables -t nat -A POSTROUTING -s 192.168.123.0/24 ! -d 192.168.123.0/24 -j MASQUERADE

rm -rf srv1 srv2 srv3
mkdir -p srv1 srv2 srv3

# Prepare cloud-init
for i in 1 2 3; do
  echo "hostname: srv$i" > "srv$i/meta-data"
  echo '#cloud-config' > "srv$i/user-data"
  cat > "srv$i/network-config" <<EOT
version: 2
ethernets:
  eth0:
    dhcp4: false
    addresses:
      - "192.168.123.1$i/26"
    gateway4: "192.168.123.1"
    nameservers:
      search: [cluster.local]
      addresses: [8.8.8.8]
EOT

  ( cd srv$i && genisoimage \
      -output seed.img \
      -volid cidata -rational-rock -joliet \
      user-data meta-data network-config
  )
done

# Prepare system drive
if [ ! -f nocloud-amd64.raw ]; then
  wget https://github.com/cozystack/cozystack/releases/latest/download/nocloud-amd64.raw.xz -O nocloud-amd64.raw.xz
  rm -f nocloud-amd64.raw
  xz --decompress nocloud-amd64.raw.xz
fi
for i in 1 2 3; do
  cp nocloud-amd64.raw srv$i/system.img
  qemu-img resize srv$i/system.img 20G
done

# Prepare data drives
for i in 1 2 3; do
  qemu-img create srv$i/data.img 100G
done

# Prepare networking
for i in 1 2 3; do
  ip link del cozy-srv$i || true
  ip tuntap add dev cozy-srv$i mode tap
  ip link set cozy-srv$i up
  ip link set cozy-srv$i master cozy-br0
done

# Start VMs
for i in 1 2 3; do
  qemu-system-x86_64 -machine type=pc,accel=kvm -cpu host -smp 8 -m 16384 \
    -device virtio-net,netdev=net0,mac=52:54:00:12:34:5$i -netdev tap,id=net0,ifname=cozy-srv$i,script=no,downscript=no \
    -drive file=srv$i/system.img,if=virtio,format=raw \
    -drive file=srv$i/seed.img,if=virtio,format=raw \
    -drive file=srv$i/data.img,if=virtio,format=raw \
    -display none -daemonize -pidfile srv$i/qemu.pid
done

sleep 5

# Wait for VM to start up
timeout 60 sh -c 'until nc -nzv 192.168.123.11 50000 && nc -nzv 192.168.123.12 50000 && nc -nzv 192.168.123.13 50000; do sleep 1; done'

cat > patch.yaml <<\EOT
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
  files:
  - content: |
      [plugins]
        [plugins."io.containerd.grpc.v1.cri"]
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
EOT

cat > patch-controlplane.yaml <<\EOT
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
EOT

# Gen configuration
if [ ! -f secrets.yaml ]; then
  talosctl gen secrets
fi

rm -f controlplane.yaml worker.yaml talosconfig kubeconfig
talosctl gen config --with-secrets secrets.yaml cozystack https://192.168.123.10:6443 --config-patch=@patch.yaml --config-patch-control-plane @patch-controlplane.yaml
export TALOSCONFIG=$PWD/talosconfig

# Apply configuration
talosctl apply -f controlplane.yaml -n 192.168.123.11 -e 192.168.123.11 -i
talosctl apply -f controlplane.yaml -n 192.168.123.12 -e 192.168.123.12 -i
talosctl apply -f controlplane.yaml -n 192.168.123.13 -e 192.168.123.13 -i

# Wait for VM to be configured
timeout 60 sh -c 'until nc -nzv 192.168.123.11 50000 && nc -nzv 192.168.123.12 50000 && nc -nzv 192.168.123.13 50000; do sleep 1; done'

# Bootstrap
timeout 10 sh -c 'until talosctl bootstrap -n 192.168.123.11 -e 192.168.123.11; do sleep 1; done'

# Wait for etcd
timeout 180 sh -c 'until timeout -s 9 2 talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.10 2>&1; do sleep 1; done'
timeout 60 sh -c 'while talosctl etcd members -n 192.168.123.11,192.168.123.12,192.168.123.13 -e 192.168.123.10 2>&1 | grep "rpc error"; do sleep 1; done'

rm -f kubeconfig
talosctl kubeconfig kubeconfig -e 192.168.123.10 -n 192.168.123.10
export KUBECONFIG=$PWD/kubeconfig

# Wait for kubernetes nodes appear
timeout 60 sh -c 'until [ $(kubectl get node -o name | wc -l) = 3 ]; do sleep 1; done'
kubectl create ns cozy-system -o yaml | kubectl apply -f -
kubectl create -f - <<\EOT
apiVersion: v1
kind: ConfigMap
metadata:
  name: cozystack
  namespace: cozy-system
data:
  bundle-name: "paas-full"
  ipv4-pod-cidr: "10.244.0.0/16"
  ipv4-pod-gateway: "10.244.0.1"
  ipv4-svc-cidr: "10.96.0.0/16"
  ipv4-join-cidr: "100.64.0.0/16"
  root-host: example.org
  api-server-endpoint: https://192.168.123.10:6443
EOT

#
echo "$COZYSTACK_INSTALLER_YAML" | kubectl apply -f -

# wait for cozystack pod to start
kubectl wait deploy  --timeout=1m --for=condition=available -n cozy-system cozystack

# wait for helmreleases appear
timeout 60 sh -c 'until kubectl get hr -A | grep cozy; do sleep 1; done'

sleep 5

kubectl get hr -A | awk 'NR>1 {print "kubectl wait --timeout=15m --for=condition=ready -n " $1 " hr/" $2 " &"} END{print "wait"}' | sh -x

# Wait for Cluster-API providers
timeout 30 sh -c 'until kubectl get deploy -n cozy-cluster-api capi-controller-manager capi-kamaji-controller-manager capi-kubeadm-bootstrap-controller-manager capi-operator-cluster-api-operator capk-controller-manager; do sleep 1; done'
kubectl wait deploy --timeout=30s --for=condition=available -n cozy-cluster-api capi-controller-manager capi-kamaji-controller-manager capi-kubeadm-bootstrap-controller-manager capi-operator-cluster-api-operator capk-controller-manager

# Wait for linstor controller
kubectl wait deploy --timeout=5m --for=condition=available -n cozy-linstor linstor-controller

# Wait for all linstor nodes become Online
timeout 60 sh -c 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) = 3 ]; do sleep 1; done'

kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs srv1 /dev/vdc --pool-name data --storage-pool data
kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs srv2 /dev/vdc --pool-name data --storage-pool data
kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs srv3 /dev/vdc --pool-name data --storage-pool data

kubectl create -f- <<EOT
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
EOT
kubectl create -f- <<EOT
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  ipAddressPools:
  - cozystack
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: cozystack
  namespace: cozy-metallb
spec:
  addresses:
  - 192.168.123.200-192.168.123.250
  autoAssign: true
  avoidBuggyIPs: false
EOT

# Wait for cozystack-api
kubectl wait --for=condition=Available apiservices v1alpha1.apps.cozystack.io --timeout=2m

kubectl patch -n tenant-root tenants.apps.cozystack.io root --type=merge -p '{"spec":{
  "host": "example.org",
  "ingress": true,
  "monitoring": true,
  "etcd": true,
  "isolated": true
}}'

# Wait for HelmRelease be created
timeout 60 sh -c 'until kubectl get hr -n tenant-root etcd ingress monitoring tenant-root; do sleep 1; done'

# Wait for HelmReleases be installed
kubectl wait --timeout=2m --for=condition=ready -n tenant-root hr etcd ingress monitoring tenant-root

kubectl patch -n tenant-root ingresses.apps.cozystack.io ingress --type=merge -p '{"spec":{
  "dashboard": true
}}'

# Wait for nginx-ingress-controller
timeout 60 sh -c 'until kubectl get deploy -n tenant-root root-ingress-controller; do sleep 1; done'
kubectl wait --timeout=5m --for=condition=available -n tenant-root deploy root-ingress-controller

# Wait for etcd
kubectl wait --timeout=5m --for=jsonpath=.status.readyReplicas=3 -n tenant-root sts etcd

# Wait for Victoria metrics
kubectl wait --timeout=5m --for=jsonpath=.status.updateStatus=operational -n tenant-root vmalert/vmalert-shortterm vmalertmanager/alertmanager
kubectl wait --timeout=5m --for=jsonpath=.status.status=operational -n tenant-root vlogs/generic
kubectl wait --timeout=5m --for=jsonpath=.status.clusterStatus=operational -n tenant-root vmcluster/shortterm vmcluster/longterm

# Wait for grafana
kubectl wait --timeout=5m --for=condition=ready -n tenant-root clusters.postgresql.cnpg.io grafana-db
kubectl wait --timeout=5m --for=condition=available -n tenant-root deploy grafana-deployment 

# Get IP of nginx-ingress
ip=$(kubectl get svc -n tenant-root root-ingress-controller -o jsonpath='{.status.loadBalancer.ingress..ip}')

# Check Grafana
curl -sS -k "https://$ip" -H 'Host: grafana.example.org' | grep Found


# Test OIDC
kubectl patch -n cozy-system cm/cozystack --type=merge -p '{"data":{
  "oidc-enabled": "true"
}}'

timeout 120 sh -c 'until kubectl get hr -n cozy-keycloak keycloak keycloak-configure keycloak-operator; do sleep 1; done'
kubectl wait --timeout=10m --for=condition=ready -n cozy-keycloak hr keycloak keycloak-configure keycloak-operator
