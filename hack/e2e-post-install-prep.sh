#!/bin/sh
# Runs LINSTOR pool + StorageClass + MetalLB pool configuration as soon as
# their respective prerequisites are reachable. Designed to run in the
# background during the platform HR reconcile wait, so its wall-clock cost
# overlaps with the wait instead of compounding it.
set -eu

echo "[post-install-prep] waiting for linstor HelmRelease object to exist"
timeout 60 sh -ec 'until kubectl get hr/linstor -n cozy-linstor >/dev/null 2>&1; do sleep 2; done'

echo "[post-install-prep] waiting for linstor HelmRelease to be Ready"
kubectl wait helmrelease/linstor -n cozy-linstor --for=condition=Ready --timeout=15m

echo "[post-install-prep] waiting for linstor-controller deployment to exist"
timeout 300 sh -ec 'until kubectl get deploy/linstor-controller -n cozy-linstor >/dev/null 2>&1; do sleep 2; done'

echo "[post-install-prep] waiting for linstor-controller to be Available"
kubectl wait deployment/linstor-controller -n cozy-linstor --timeout=5m --for=condition=available

echo "[post-install-prep] waiting for 3 LINSTOR nodes Online"
# TODO(e2e-replace-fixed-timeouts): genuine poll. LINSTOR node membership is
# reported by the linstor binary inside the controller pod, not via a
# Kubernetes API condition, so kubectl wait cannot subscribe to it.
timeout 60 sh -ec 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) -eq 3 ]; do sleep 1; done'

echo "[post-install-prep] creating LINSTOR storage pools (parallel across nodes)"
created_pools=$(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor sp l -s data --pastable | awk '$2 == "data" {printf " " $4} END{printf " "}')
for node in srv1 srv2 srv3; do
  case $created_pools in
    *" $node "*) echo "  pool 'data' already exists on $node"; continue;;
  esac
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs ${node} /dev/vdc --pool-name data --storage-pool data &
done
wait

echo "[post-install-prep] applying StorageClasses"
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
volumeBindingMode: Immediate
allowVolumeExpansion: true
EOF

echo "[post-install-prep] waiting for MetalLB CRDs"
timeout 300 sh -ec 'until kubectl get crd ipaddresspools.metallb.io l2advertisements.metallb.io >/dev/null 2>&1; do sleep 2; done'

echo "[post-install-prep] applying MetalLB IPAddressPool"
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

echo "[post-install-prep] done"
