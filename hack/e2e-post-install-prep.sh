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
# failed. So every wait below tolerates a not-yet-created object without
# aborting, and each gets its own budget, started when the link before it
# completed rather than when this script did -- the same shape the LINSTOR
# node and MetalLB CRD waits further down already have.
#
# One budget shared by the whole chain instead fails the install whenever the
# chain merely runs long, because the head of it eats the window and the tail
# inherits the remainder. Both halves were observed inside a single 15m window
# on one run: the linstor HR went Ready 452s in, and the Deployment needed a
# further ~520s because the controller's DB migration lost its connection to
# the apiserver and retried four times before succeeding. Neither figure is
# anomalous and neither exceeds the budget on its own; only their sum did, and
# the install failed naming the Deployment as though it were broken.
#
# What a per-link budget buys is not speed but a guarantee: every link gets its
# whole allowance no matter what the links ahead of it spent, so the tail of the
# chain can no longer be failed by the head merely being slow. Detecting a link
# that is genuinely stuck does not get faster, and for the second link it gets
# slower, because its budget starts when the first one finished: a Deployment
# that never converges is reported later by however long the HelmRelease took.
# A first link that never becomes Ready is reported at the same moment as
# before, since its budget still starts with the script.
#
# The cost is the ceiling: two waits at LINK_BUDGET each put the worst case at
# twice that figure rather than once, ahead of the 300s node wait and the 300s
# MetalLB wait below, and still well inside the timeout the job carries.
set -eu

# Per-link budget in seconds, applied by wait_for_linstor to each link separately.
LINK_BUDGET=900

# wait_for_linstor <description> <kubectl-wait-args...>
# Polls `kubectl wait` until it succeeds or this link's budget elapses. The
# name says linstor because the timeout diagnostics below read cozy-linstor
# unconditionally: the wait itself is generic over its kubectl arguments, but
# what it dumps on the way out is not, and a caller elsewhere would get the
# wrong namespace's pods presented as evidence.
# kubectl wait exits non-zero immediately when the object does not exist yet,
# so the loop tolerates "not created yet" without the set -e cliff that a bare
# `kubectl wait` would trigger on a NotFound. The per-attempt timeout shrinks
# to the budget remaining, so the final attempt can consume the rest of it.
wait_for_linstor() {
  desc=$1
  shift
  echo "[post-install-prep] waiting for ${desc}"
  deadline=$(( $(date +%s) + LINK_BUDGET ))
  while :; do
    remaining=$(( deadline - $(date +%s) ))
    if [ "$remaining" -le 0 ]; then
      echo "[post-install-prep] timed out after ${LINK_BUDGET}s waiting for ${desc}" >&2
      # The description names the object waited on, not the link of the chain
      # that held it up, and those differ: the same "Deployment not Available"
      # has been reached with the controller pod crash-looping its migration
      # init container and with that pod unschedulable because cert-manager had
      # not issued its client TLS Secret yet. The pod list separates the two,
      # and the namespace events carry the reason a volume or an image failed.
      # `kubectl events` rather than `kubectl get events`: it orders by when an
      # event was last seen, so a condition that has been repeating for minutes
      # -- which is what a stuck mount looks like -- stays inside the window
      # instead of being pushed out of it by newer one-off events. It also has
      # no --sort-by, so it cannot fail the whole read the way a sort key does
      # when it is absent from any single item; .lastTimestamp is unset on an
      # Event written through events.k8s.io/v1, and that failure prints nothing
      # at all. Both reads keep their stderr, because a diagnostic that fails
      # silently is indistinguishable from a namespace with nothing to report
      # -- the very distinction being drawn here. Both are bounded, since the
      # caller is blocked in `wait` and a wedged apiserver would otherwise hold
      # the install open until the job's own timeout; the client budget is
      # strictly the smaller of the two, so kubectl gets to name the reason
      # before the outer kill takes it away.
      timeout -k 5 30 kubectl get pods -n cozy-linstor -o wide \
        --request-timeout=10s 2>&1 | tail -n 30 >&2
      timeout -k 5 30 kubectl events -n cozy-linstor \
        --request-timeout=10s 2>&1 | tail -n 30 >&2
      return 1
    fi
    if kubectl wait "$@" --timeout="${remaining}s" 2>/dev/null; then
      return 0
    fi
    sleep 5
  done
}

# controller_reachable: true only while the linstor-controller Service has at
# least one *ready* endpoint. The linstor CLI below dials that Service
# (linstor+ssl://linstor-controller:3371), and a ClusterIP is routable only once
# its Service has a ready backend -- otherwise the dial fails with
# "[Errno 113] No route to host" (EHOSTUNREACH). In the core Endpoints object the
# ready set is .subsets[].addresses (peers not yet ready sit in
# .subsets[].notReadyAddresses), so a non-empty addresses list is exactly
# ">=1 ready backend". Missing object / API error -> empty -> not reachable.
controller_reachable() {
  [ -n "$(kubectl get endpoints linstor-controller -n cozy-linstor \
    -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)" ]
}

wait_for_linstor "linstor HelmRelease to be Ready" \
  helmrelease/linstor -n cozy-linstor --for=condition=Ready
wait_for_linstor "linstor-controller Deployment to be Available" \
  deployment/linstor-controller -n cozy-linstor --for=condition=available

# Wait for 3 satellites to register Online, but gate every probe on a ready
# controller Service endpoint. The "Deployment Available" check above is a
# one-shot gate that goes stale: the controller carries
# reloader.stakater.com/auto=true and mounts the cert-manager-issued API-TLS
# Secret (see packages/system/linstor/templates/cluster.yaml). When cert-manager
# (re)writes that Secret during bring-up, reloader rolls the single-replica
# controller, dropping its Service to zero ready endpoints *after* Available
# already passed. A blind CLI dial into that window is what surfaced as the
# tolerated "[Errno 113] No route to host" churn. Probing only while the Service
# is routable removes that churn at the root -- a mid-loop reload re-waits for
# the endpoint instead of erroring -- while "Online == 3" stays the real
# satellite-convergence assertion. The dedicated 300s budget is preserved from
# the previous `timeout 300`; `set -e` is disabled inside an until-condition, so
# a not-yet-reachable controller does not abort the script.
echo "[post-install-prep] waiting for linstor-controller endpoint + 3 LINSTOR nodes Online"
node_deadline=$(( $(date +%s) + 300 ))
until controller_reachable \
  && [ "$(kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list 2>/dev/null | grep -c Online)" -eq 3 ]; do
  if [ "$(date +%s)" -ge "$node_deadline" ]; then
    echo "[post-install-prep] timed out waiting for linstor-controller endpoint + 3 LINSTOR nodes Online" >&2
    kubectl get endpoints linstor-controller -n cozy-linstor -o wide >&2 || true
    kubectl get pods -n cozy-linstor -o wide >&2 || true
    exit 1
  fi
  sleep 2
done

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
