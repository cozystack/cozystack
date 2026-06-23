#!/bin/sh
# Runs Blockstor pool + StorageClass + MetalLB pool configuration as soon
# as their respective prerequisites are reachable. Designed to run in the
# background during the platform HR reconcile wait, so its wall-clock cost
# overlaps with the wait instead of compounding it.
#
# PoC: LINSTOR was replaced by Blockstor. piraeus-operator runs in
# EXTERNAL mode (no in-cluster linstor-controller to exec into); the
# storage backend is the blockstor controller/apiserver/satellite stack.
# We create the backing `data` zpool on each worker's /dev/vdc directly
# inside the blockstor-satellite pod, then declare a per-node StoragePool
# CRD named `data` backed by that zpool. The StorageClasses are unchanged
# — blockstor serves the same csi wire-shape, so provisioner
# linstor.csi.linbit.com + storagePool: data keeps working.
set -eu

NS=cozy-linstor
ZPOOL=data        # backing ZFS zpool name on the node
POOL=data         # blockstor StoragePool name (matches the SC storagePool param)
DISK=/dev/vdc     # spare data disk on every worker

# Comprehensive failure diagnostics. Fired via `trap diag EXIT` so that
# ANY non-zero exit (whichever wait/step failed) dumps the full state of
# the storage stack in one shot — pods, the not-ready blockstor-* and
# linstor-* pods, the piraeus CRs, nodes, the controller/apiserver/
# satellite logs (including piraeus's own linstor-satellite DaemonSet,
# which crashes on Talos without the talos-loader-override), and the
# certs/secrets. A single failed run then reveals every current gap.
diag() {
  rc=$?
  [ "$rc" -eq 0 ] && return 0
  echo "[post-install-prep] FAILURE (exit $rc) — dumping comprehensive diagnostics" >&2
  echo "===== pods -n $NS (wide) ====="
  kubectl -n "$NS" get pods -o wide 2>&1 || true

  echo "----- describe ds/blockstor-satellite + scheduling events -----"
  kubectl -n "$NS" describe ds blockstor-satellite 2>&1 | tail -60 || true
  kubectl -n "$NS" get events --sort-by=.lastTimestamp 2>&1 | grep -i "blockstor-satellite" | tail -20 || true
  echo "===== describe not-ready blockstor-* / linstor-* pods -n $NS ====="
  not_ready=$(kubectl -n "$NS" get pods --no-headers 2>/dev/null \
    | awk '$2 !~ /^([0-9]+)\/\1$/ || $3 != "Running" {print $1}' \
    | grep -E '^(blockstor-|linstor-)' || true)
  for p in $not_ready; do
    echo "----- describe pod $p -----"
    kubectl -n "$NS" describe pod "$p" 2>&1 || true
  done
  echo "===== linstorcluster,linstorsatelliteconfiguration -o yaml (head 200) ====="
  kubectl -n "$NS" get linstorcluster,linstorsatelliteconfiguration -o yaml 2>&1 | head -200 || true
  echo "===== nodes (wide) ====="
  kubectl get nodes -o wide 2>&1 || true
  echo "===== logs deploy/blockstor-apiserver (tail 100) ====="
  kubectl -n "$NS" logs deploy/blockstor-apiserver --tail=100 2>&1 || true
  echo "===== logs deploy/blockstor-controller (tail 100) ====="
  kubectl -n "$NS" logs deploy/blockstor-controller --tail=100 2>&1 || true
  echo "===== logs ds/blockstor-satellite (all pods, all containers, tail 100) ====="
  kubectl -n "$NS" logs ds/blockstor-satellite --all-containers --prefix --tail=100 2>&1 || true
  if kubectl -n "$NS" get ds/linstor-satellite >/dev/null 2>&1; then
    echo "===== logs ds/linstor-satellite (all pods, all containers, tail 100) ====="
    kubectl -n "$NS" logs ds/linstor-satellite --all-containers --prefix --tail=100 2>&1 || true
  fi
  echo "===== logs deploy/linstor-csi-controller (all containers, tail 100) ====="
  kubectl -n "$NS" logs deploy/linstor-csi-controller --all-containers --tail=100 2>&1 || true
  echo "===== certificate,secret -n $NS ====="
  kubectl -n "$NS" get certificate,secret 2>&1 || true
  echo "[post-install-prep] end of diagnostics (exit $rc)" >&2
}
trap diag EXIT

echo "[post-install-prep] waiting for linstor HelmRelease object to exist"
timeout 60 sh -ec 'until kubectl get hr/linstor -n '"$NS"' >/dev/null 2>&1; do sleep 2; done'

echo "[post-install-prep] waiting for linstor HelmRelease to be Ready"
kubectl wait helmrelease/linstor -n "$NS" --for=condition=Ready --timeout=15m

echo "[post-install-prep] waiting for blockstor-apiserver deployment to exist"
timeout 300 sh -ec 'until kubectl get deploy/blockstor-apiserver -n '"$NS"' >/dev/null 2>&1; do sleep 2; done'

echo "[post-install-prep] waiting for blockstor-apiserver to be Available"
kubectl wait deployment/blockstor-apiserver -n "$NS" --timeout=5m --for=condition=available

echo "[post-install-prep] waiting for blockstor-controller to be Available"
kubectl wait deployment/blockstor-controller -n "$NS" --timeout=5m --for=condition=available

echo "[post-install-prep] waiting for LinstorCluster Available (external mode)"
if ! timeout 600 sh -ec 'until kubectl get linstorcluster linstorcluster -o jsonpath="{.status.conditions[?(@.type==\"Available\")].status}" 2>/dev/null | grep -q True; do sleep 5; done'; then
  echo "[post-install-prep] ERROR: LinstorCluster did not become Available within 600s" >&2
  exit 1
fi

echo "[post-install-prep] waiting for 3 blockstor-satellite pods Ready"
timeout 300 sh -ec 'until [ $(kubectl -n '"$NS"' get pods -l app=blockstor-satellite --no-headers 2>/dev/null | awk "{print \$2}" | grep -c "^1/1$") -eq 3 ]; do sleep 5; done'

echo "[post-install-prep] registering blockstor Node CRs (netInterfaces address=InternalIP) for DRBD peer resolution"
# Cluster-scoped blockstor.cozystack.io/Node CRs must be pre-created with each
# worker's InternalIP under spec.netInterfaces — the controller uses these for
# DRBD peer address resolution on replicated (multi-replica) volumes. The
# node_label_sync controller only PATCHES existing Node CRs (skips on NotFound),
# so without this step replicated PVCs never reach UpToDate. Mirrors blockstor
# stand/install-blockstor.sh. Idempotent (kubectl apply).
for node in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'); do
    ip=$(kubectl get node "$node" -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
    kubectl apply -f - <<EOF
apiVersion: blockstor.cozystack.io/v1alpha1
kind: Node
metadata:
  name: $node
spec:
  type: SATELLITE
  netInterfaces:
    - {name: default, address: $ip}
EOF
done

echo "[post-install-prep] creating '$ZPOOL' zpool on $DISK + StoragePool '$POOL' (parallel across satellites)"
pids=""
for pod in $(kubectl -n "$NS" get pods -l app=blockstor-satellite -o jsonpath='{.items[*].metadata.name}'); do
  (
    node=$(kubectl -n "$NS" get pod "$pod" -o jsonpath='{.spec.nodeName}')
    # Create the backing zpool inside the satellite pod (it has the
    # privileged /dev + /run/udev + /lib/modules mounts libzfs needs).
    # Idempotent: skip if the pool already exists. Mirrors blockstor
    # stand/install-pools.sh create_zfs — partition first, then hand
    # zpool the partition path (whole-disk zpool create's GPT-rescan
    # fails inside the container's devtmpfs view).
    kubectl -n "$NS" exec "$pod" -- sh -ec '
      if zpool list '"$ZPOOL"' >/dev/null 2>&1; then
        echo "zpool '"$ZPOOL"' already exists on '"$node"'"
        exit 0
      fi
      wipefs -af '"$DISK"'* 2>/dev/null || true
      sgdisk --zap-all '"$DISK"' 2>/dev/null || true
      sgdisk --new=1:0:0 -t 1:bf01 '"$DISK"'
      partprobe '"$DISK"' 2>/dev/null || true
      sleep 1
      zpool create -f -o cachefile=none '"$ZPOOL"' '"$DISK"'1
      echo "zpool '"$ZPOOL"' created on '"$node"'"
    '
    # Declare the StoragePool CRD. The CRD CEL rule pins
    # metadata.name == <poolName>.<nodeName> (lowercased), so the name
    # MUST be data.<node>. StorDriver/ZPoolThin points at the zpool.
    kubectl apply -f - <<EOF
apiVersion: blockstor.cozystack.io/v1alpha1
kind: StoragePool
metadata:
  name: ${POOL}.${node}
spec:
  nodeName: ${node}
  poolName: ${POOL}
  providerKind: ZFS_THIN
  props:
    StorDriver/ZPoolThin: ${ZPOOL}
EOF
  ) &
  pids="$pids $!"
done
for pid in $pids; do
  wait "$pid"
done

echo "[post-install-prep] StoragePools:"
kubectl get storagepools -o wide 2>/dev/null || true

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
