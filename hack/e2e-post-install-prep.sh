#!/bin/sh
# Runs LINSTOR pool + StorageClass + MetalLB pool configuration as soon as
# their respective prerequisites are reachable. Designed to run in the
# background during the platform HR reconcile wait, so its wall-clock cost
# overlaps with the wait instead of compounding it.
#
# Each LINSTOR prerequisite below sits at the end of a multi-hop reconcile
# chain: cozystack-operator -> platform HR -> linstor HR -> piraeus-operator
# -> cert-manager issues the controller TLS -> linstor-controller Deployment
# -> controller pod -> DB migration. On a loaded CI runner that chain has been
# observed to take 7-9 min end to end, and the operator alone needs ~70s just
# to emit the linstor HR. The earlier per-step "object exists" budgets
# (timeout 60 / timeout 300) were anchored to this script's start, so they
# raced that reconcile latency; when one lost (linstor HR appeared at ~+70s
# against a 60s budget) `set -e` aborted the whole script and the install
# failed. Instead, drive every wait off one shared deadline -- the same 15m
# window the installer's `kubectl wait hr --all` uses -- and tolerate
# not-yet-created objects without aborting, while still failing hard if a
# resource never becomes ready inside the budget.
set -eu

DEADLINE=$(( $(date +%s) + 900 ))

# wait_for <description> <kubectl-wait-args...>
# Polls `kubectl wait` until it succeeds or the shared deadline elapses.
# kubectl wait exits non-zero immediately when the object does not exist yet,
# so the loop tolerates "not created yet" without the set -e cliff that a bare
# `kubectl wait` would trigger on a NotFound. The per-attempt timeout shrinks
# to the budget remaining, so the final attempt can consume the rest of it.
wait_for() {
  desc=$1
  shift
  echo "[post-install-prep] waiting for ${desc}"
  while :; do
    remaining=$(( DEADLINE - $(date +%s) ))
    if [ "$remaining" -le 0 ]; then
      echo "[post-install-prep] timed out waiting for ${desc}" >&2
      return 1
    fi
    if kubectl wait "$@" --timeout="${remaining}s" 2>/dev/null; then
      return 0
    fi
    sleep 5
  done
}

wait_for "linstor HelmRelease to be Ready" \
  helmrelease/linstor -n cozy-linstor --for=condition=Ready
wait_for "linstor-controller Deployment to be Available" \
  deployment/linstor-controller -n cozy-linstor --for=condition=available

echo "[post-install-prep] waiting for 3 LINSTOR nodes Online"
timeout 300 sh -ec 'until [ $(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list | grep -c Online) -eq 3 ]; do sleep 2; done'

echo "[post-install-prep] creating LINSTOR storage pools (parallel across nodes)"
created_pools=$(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor sp l -s data --pastable | awk '$2 == "data" {printf " " $4} END{printf " "}')
pids=""
for node in srv1 srv2 srv3; do
  case $created_pools in
    *" $node "*) echo "  pool 'data' already exists on $node"; continue;;
  esac
  kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor ps cdp zfs ${node} /dev/vdc --pool-name data --storage-pool data &
  pids="$pids $!"
done
for pid in $pids; do
  wait "$pid"
done

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
