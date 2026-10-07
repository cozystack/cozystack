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
# twice that figure rather than once, ahead of the 300s node wait, the 600s
# StorageProfile wait in container mode and the 300s MetalLB wait below, plus
# the bound on each call that does work, and still well inside the timeout the
# job carries.
set -eu

# The QEMU lane gives every satellite a private /dev/vdc and lets LINSTOR
# create the zpool. Container nodes share the runner kernel, where zpools are
# global, so hack/e2e-container-up.sh creates one node-distinct pool ahead of
# time and the satellite must register that existing pool instead.
validate_linstor_storage_mode() {
  case "${COZY_LINSTOR_DRBD_ENABLED:-true}" in
    true | false) return 0 ;;
    *)
      echo "[post-install-prep] COZY_LINSTOR_DRBD_ENABLED must be true or false, got '${COZY_LINSTOR_DRBD_ENABLED}'" >&2
      return 1
      ;;
  esac
}

# Which storage control plane the install deployed. The caller passes what it
# installed rather than this script guessing: a probe run here would race the
# install it runs alongside, since the namespace it would look in does not
# exist yet when the script starts, and it would fail open to linstor on any
# error. An unknown value is refused for the same reason the platform chart
# refuses one: it would otherwise take whichever branch is the fallback.
validate_storage_backend() {
  case "${COZY_STORAGE_BACKEND:-}" in
    linstor | blockstor) return 0 ;;
    "")
      echo "[post-install-prep] COZY_STORAGE_BACKEND must be set to linstor or blockstor" >&2
      return 1
      ;;
    *)
      echo "[post-install-prep] COZY_STORAGE_BACKEND must be linstor or blockstor, got '${COZY_STORAGE_BACKEND}'" >&2
      return 1
      ;;
  esac
}

# KUBECTL_BOUND prefixes the cluster calls in the helpers below with a wall-clock
# bound. The main body sets it; it stays empty when unit tests source the
# helpers, because `timeout` execs a binary and would bypass the kubectl shell
# function those tests substitute, reaching whatever cluster the host points at.
KUBECTL_BOUND=

create_linstor_storage_pool() {
  e2e_linstor_node=$1
  case "${COZY_LINSTOR_DRBD_ENABLED:-true}" in
    true)
      $KUBECTL_BOUND kubectl exec -n cozy-linstor deploy/linstor-controller -- \
        linstor physical-storage create-device-pool zfs "$e2e_linstor_node" /dev/vdc \
        --pool-name data --storage-pool data
      ;;
    false)
      $KUBECTL_BOUND kubectl exec -n cozy-linstor deploy/linstor-controller -- \
        linstor storage-pool create zfs "$e2e_linstor_node" data "data-$e2e_linstor_node"
      ;;
    *) return 2 ;;
  esac
}

render_linstor_storageclasses() {
  cat <<'EOF'
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
EOF

  if [ "${COZY_LINSTOR_DRBD_ENABLED:-true}" = false ]; then
    return 0
  fi

  cat <<'EOF'
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
}

# CDI infers ReadWriteMany/Block first for the LINSTOR provisioner because it
# assumes a DRBD-capable StorageClass. The container lane deliberately exposes
# only LINSTOR's local storage layer, and linstor-csi rejects RWX without DRBD.
# Pin the one class used by that lane to the RWO/Block combination phase-0
# proved before any DataVolume can consume the inferred default.
patch_local_cdi_storage_profile() {
  $KUBECTL_BOUND kubectl patch storageprofile local --request-timeout=60s --type merge \
    -p '{"spec":{"claimPropertySets":[{"accessModes":["ReadWriteOnce"],"volumeMode":"Block"}]}}'
}

# count_true <text>: how many lines of <text> are exactly True.
count_true() {
  printf '%s\n' "$1" | grep -c '^True$' || true
}

# count_lines <text>: how many non-empty lines <text> has.
count_lines() {
  printf '%s\n' "$1" | grep -c . || true
}

render_blockstor_node() {
  cat <<EOF
apiVersion: blockstor.cozystack.io/v1alpha1
kind: Node
metadata:
  name: $1
spec:
  type: SATELLITE
  netInterfaces:
    - {name: default, address: $2}
EOF
}

# The CRD's CEL rule pins metadata.name to <poolName>.<nodeName>.
render_blockstor_storage_pool() {
  cat <<EOF
apiVersion: blockstor.cozystack.io/v1alpha1
kind: StoragePool
metadata:
  name: data.$1
spec:
  nodeName: $1
  poolName: data
  providerKind: ZFS_THIN
  props:
    StorDriver/ZPoolThin: data
EOF
}

# Unit tests source the pure helpers above without reaching a cluster.
if [ "${E2E_POST_INSTALL_PREP_LIB:-false}" = true ]; then
  return 0 2>/dev/null || exit 0
fi

if ! validate_linstor_storage_mode; then
  exit 2
fi

if ! validate_storage_backend; then
  exit 2
fi

# The blockstor branch creates its zpool on the QEMU lane's private /dev/vdc.
# The container lane has no such disk, and runs no DRBD for blockstor to drive.
if [ "$COZY_STORAGE_BACKEND" = blockstor ] && [ "${COZY_LINSTOR_DRBD_ENABLED:-true}" = false ]; then
  echo "[post-install-prep] COZY_STORAGE_BACKEND=blockstor is not supported with COZY_LINSTOR_DRBD_ENABLED=false" >&2
  exit 2
fi

# Every call that does work carries a bound, not only the waits: the caller
# blocks in `wait` on this script, so one call that never returns holds the
# install open until the job's own ceiling instead of failing the step. A
# deadline checked between loop iterations does not cover the call inside the
# iteration. Plain requests also carry a --request-timeout strictly inside the
# outer bound, so kubectl gets to name the reason before the kill lands; exec
# streams get the outer bound alone. When that bound fires, `timeout` prints
# nothing and kubectl dies on the signal before it can, so the wrapper says
# which call it was: otherwise the log ends on the step's announcement line.
# Only 124 is named. 137 is also what `kubectl exec` returns when the remote
# command is OOM-killed, so reading it as this bound would name the wrong cause.
CALL_BOUND=300
bounded() {
  bounded_rc=0
  timeout -k 5 "$CALL_BOUND" "$@" || bounded_rc=$?
  if [ "$bounded_rc" -eq 124 ]; then
    echo "[post-install-prep] killed after ${CALL_BOUND}s: $*" >&2
  fi
  return "$bounded_rc"
}
KUBECTL_BOUND=bounded

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
# The outer `timeout` sits above that: --timeout covers only the watch, not the
# GET that resolves the object first, which has no deadline of its own.
# An expired credential or an RBAC denial exits the same way, and only the
# text tells them apart, so stderr is kept and printed whenever it changes:
# once for a NotFound repeated every poll, and again when the cause changes.
# It is printed rather than matched to fail fast, because apiserver restarts
# during install produce the same connection and auth errors transiently.
wait_for_linstor() {
  desc=$1
  shift
  echo "[post-install-prep] waiting for ${desc}"
  deadline=$(( $(date +%s) + LINK_BUDGET ))
  last_err=
  while :; do
    remaining=$(( deadline - $(date +%s) ))
    if [ "$remaining" -le 0 ]; then
      echo "[post-install-prep] timed out after ${LINK_BUDGET}s waiting for ${desc}; last kubectl error: ${last_err:-none}" >&2
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
      # at all. Every read here keeps its stderr, because a diagnostic that fails
      # silently is indistinguishable from a namespace with nothing to report
      # -- the very distinction being drawn here. Each is bounded, since the
      # caller is blocked in `wait` and a wedged apiserver would otherwise hold
      # the install open until the job's own timeout; the client budget is
      # strictly the smaller of the two, so kubectl gets to name the reason
      # before the outer kill takes it away.
      # The release list comes first: a linstor HelmRelease that never goes
      # Ready can be held by a dependency further up the chain rather than
      # failing itself, and each release's Ready message names what it waits
      # on, so the list points at the release that is actually stuck, which
      # the cozy-linstor pods cannot.
      timeout -k 5 30 kubectl get hr -A \
        --request-timeout=10s 2>&1 | tail -n 300 >&2
      timeout -k 5 30 kubectl get pods -n cozy-linstor -o wide \
        --request-timeout=10s 2>&1 | tail -n 30 >&2
      timeout -k 5 30 kubectl events -n cozy-linstor \
        --request-timeout=10s 2>&1 | tail -n 30 >&2
      return 1
    fi
    attempt_rc=0
    { attempt_err=$(timeout -k 5 $(( remaining + 30 )) \
      kubectl wait "$@" --timeout="${remaining}s" 2>&1 >&3); } 3>&1 || attempt_rc=$?
    if [ "$attempt_rc" -eq 0 ]; then
      return 0
    fi
    # A bound that fires leaves no text: `timeout` prints nothing and kubectl
    # dies on the signal. Not a remote exit code here, so 137 is the -k kill.
    case $attempt_rc in
      124 | 137) attempt_err="attempt cut by its outer bound after $(( remaining + 30 ))s" ;;
    esac
    if [ -n "$attempt_err" ] && [ "$attempt_err" != "$last_err" ]; then
      printf '%s\n' "$attempt_err" | sed 's/^/[post-install-prep] /' >&2
      last_err=$attempt_err
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
  [ -n "$(timeout -k 5 30 kubectl get endpoints linstor-controller -n cozy-linstor \
    --request-timeout=10s -o jsonpath='{.subsets[*].addresses[*].ip}' 2>/dev/null)" ]
}

# wait_for_object <budget-seconds> <description> <kubectl-get-args...>
# Polls until `kubectl get` finds the objects. NotFound is the ordinary state
# while polling, so each attempt's stderr is dropped; on the way out the read
# runs once more with stderr kept, so a timeout says which object is missing
# instead of ending on the "waiting for" line.
wait_for_object() {
  budget=$1
  desc=$2
  shift 2
  echo "[post-install-prep] waiting for ${desc}"
  deadline=$(( $(date +%s) + budget ))
  until timeout -k 5 30 kubectl get "$@" --request-timeout=10s >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      echo "[post-install-prep] timed out after ${budget}s waiting for ${desc}" >&2
      timeout -k 5 30 kubectl get "$@" --request-timeout=10s 2>&1 | tail -n 30 >&2
      return 1
    fi
    sleep 2
  done
}

wait_for_linstor "linstor HelmRelease to be Ready" \
  helmrelease/linstor -n cozy-linstor --for=condition=Ready

echo "[post-install-prep] storage backend: $COZY_STORAGE_BACKEND"

if [ "$COZY_STORAGE_BACKEND" = linstor ]; then

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
  && [ "$(timeout -k 5 30 kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor node list 2>/dev/null | grep -c Online)" -eq 3 ]; do
  if [ "$(date +%s)" -ge "$node_deadline" ]; then
    echo "[post-install-prep] timed out waiting for linstor-controller endpoint + 3 LINSTOR nodes Online" >&2
    timeout -k 5 30 kubectl get endpoints linstor-controller -n cozy-linstor -o wide \
      --request-timeout=10s 2>&1 | tail -n 30 >&2
    timeout -k 5 30 kubectl get pods -n cozy-linstor -o wide \
      --request-timeout=10s 2>&1 | tail -n 30 >&2
    exit 1
  fi
  sleep 2
done

echo "[post-install-prep] creating LINSTOR storage pools (parallel across nodes)"
# Read on its own rather than piped into awk, which would hide its status: a
# listing that failed or timed out reads as "no pools yet", and the creates it
# triggers then fail on the pools that do exist, naming the wrong reason.
pool_list=$($KUBECTL_BOUND kubectl exec -n cozy-linstor deploy/linstor-controller -- linstor sp l -s data --pastable)
created_pools=$(printf '%s\n' "$pool_list" | awk '$2 == "data" {printf " " $4} END{printf " "}')
pids=""
for node in srv1 srv2 srv3; do
  case $created_pools in
    *" $node "*) echo "  pool 'data' already exists on $node"; continue;;
  esac
  create_linstor_storage_pool "$node" &
  pids="$pids $!"
done
for pid in $pids; do
  wait "$pid"
done

else # COZY_STORAGE_BACKEND = blockstor

# piraeus-operator runs in external mode here: there is no in-cluster
# linstor-controller to exec into. The zpool is made inside the
# blockstor-satellite pod, which carries the /dev, /run/udev and /lib/modules
# mounts libzfs needs, and the pool is then declared as a StoragePool.
wait_for_linstor "blockstor-apiserver Deployment to be Available" \
  deployment/blockstor-apiserver -n cozy-linstor --for=condition=available
wait_for_linstor "blockstor-controller Deployment to be Available" \
  deployment/blockstor-controller -n cozy-linstor --for=condition=available
wait_for_linstor "LinstorCluster to be Available" \
  linstorcluster/linstorcluster --for=condition=Available

# Available says the operator reached the API once. It does not say a
# satellite reconcile finished, and the two came apart in the field: the
# operator probes /v1/controller/version before every LinstorSatellite
# reconcile, so a probe it cannot complete leaves each satellite at
# Applied=False with its DaemonSet frozen at a spec the chart no longer
# renders, while every HelmRelease reports Ready. That surfaces only when a pod
# is recreated, on a node that then loses its satellite for good, so the
# per-satellite condition is asserted here.
#
# Both reads are checked: a gate comparing two counts passes on no data when
# both reads fail and each count comes out zero. A cluster with no node is
# not a pass either.
echo "[post-install-prep] waiting for every LinstorSatellite to report Applied"
applied_deadline=$(( $(date +%s) + 300 ))
while :; do
  sat_rc=0
  sat_applied=$(timeout -k 5 30 kubectl get linstorsatellite --request-timeout=10s \
    -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Applied")].status}{"\n"}{end}' \
    2>/dev/null) || sat_rc=$?
  node_rc=0
  node_names=$(timeout -k 5 30 kubectl get nodes --request-timeout=10s \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null) || node_rc=$?
  if [ "$sat_rc" -eq 0 ] && [ "$node_rc" -eq 0 ]; then
    node_count=$(count_lines "$node_names")
    applied_count=$(count_true "$sat_applied")
    if [ "$node_count" -gt 0 ] && [ "$applied_count" -eq "$node_count" ]; then
      break
    fi
    applied_state="$applied_count of $node_count LinstorSatellite objects Applied"
  else
    applied_state="LinstorSatellite read exited $sat_rc, Node read exited $node_rc"
  fi
  if [ "$(date +%s)" -ge "$applied_deadline" ]; then
    echo "[post-install-prep] timed out after 300s waiting for every LinstorSatellite to report Applied: $applied_state" >&2
    timeout -k 5 30 kubectl get linstorsatellite --request-timeout=10s \
      -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.status.conditions[?(@.type=="Applied")].message}{"\n"}{end}' \
      2>&1 | tail -n 30 >&2
    exit 1
  fi
  sleep 5
done

echo "[post-install-prep] waiting for a Ready blockstor-satellite pod on each of $node_count nodes"
sat_deadline=$(( $(date +%s) + 300 ))
while :; do
  ready_rc=0
  sat_ready=$(timeout -k 5 30 kubectl get pods -n cozy-linstor -l app=blockstor-satellite \
    --request-timeout=10s \
    -o jsonpath='{range .items[*]}{.status.conditions[?(@.type=="Ready")].status}{"\n"}{end}' \
    2>/dev/null) || ready_rc=$?
  if [ "$ready_rc" -eq 0 ] && [ "$(count_true "$sat_ready")" -eq "$node_count" ]; then
    break
  fi
  if [ "$(date +%s)" -ge "$sat_deadline" ]; then
    echo "[post-install-prep] timed out after 300s waiting for $node_count Ready blockstor-satellite pods (read exited $ready_rc)" >&2
    timeout -k 5 30 kubectl get pods -n cozy-linstor -l app=blockstor-satellite -o wide \
      --request-timeout=10s 2>&1 | tail -n 30 >&2
    exit 1
  fi
  sleep 5
done

# Node CRs carrying each node's InternalIP must exist before replicated volumes
# can resolve their DRBD peers: the label-sync controller only patches Node CRs
# that already exist. The reads are assignments, not `for` lists, so a failed
# one stops the script under set -e instead of registering nothing and going on.
echo "[post-install-prep] registering blockstor Node CRs for DRBD peer resolution"
node_rows=$($KUBECTL_BOUND kubectl get nodes --request-timeout=60s \
  -o jsonpath='{range .items[*]}{.metadata.name} {.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}')
if [ "$(count_lines "$node_rows")" -ne "$node_count" ]; then
  echo "[post-install-prep] expected $node_count nodes with an address, read: $node_rows" >&2
  exit 1
fi
while read -r node ip; do
  [ -n "$node" ] || continue
  if [ -z "$ip" ]; then
    echo "[post-install-prep] node $node has no InternalIP" >&2
    exit 1
  fi
  render_blockstor_node "$node" "$ip" | $KUBECTL_BOUND kubectl apply --request-timeout=60s -f -
done <<EOF
$node_rows
EOF

echo "[post-install-prep] creating the 'data' zpool and StoragePool on each satellite (parallel)"
sat_pods=$($KUBECTL_BOUND kubectl get pods -n cozy-linstor -l app=blockstor-satellite \
  --request-timeout=60s -o jsonpath='{range .items[*]}{.metadata.name} {.spec.nodeName}{"\n"}{end}')
if [ "$(count_lines "$sat_pods")" -ne "$node_count" ]; then
  echo "[post-install-prep] expected $node_count blockstor-satellite pods, read: $sat_pods" >&2
  exit 1
fi
pids=""
while read -r pod node; do
  [ -n "$pod" ] || continue
  (
    # Partition first and hand zpool the partition: a whole-disk create needs
    # a GPT rescan that fails inside the container's devtmpfs view.
    $KUBECTL_BOUND kubectl exec -n cozy-linstor "$pod" -- sh -ec '
      if zpool list data >/dev/null 2>&1; then
        echo "zpool data already exists on '"$node"'"
        exit 0
      fi
      wipefs -af /dev/vdc* 2>/dev/null || true
      sgdisk --zap-all /dev/vdc 2>/dev/null || true
      sgdisk --new=1:0:0 -t 1:bf01 /dev/vdc
      partprobe /dev/vdc 2>/dev/null || true
      sleep 1
      zpool create -f -o cachefile=none data /dev/vdc1
      echo "zpool data created on '"$node"'"
    '
    render_blockstor_storage_pool "$node" | $KUBECTL_BOUND kubectl apply --request-timeout=60s -f -
  ) &
  pids="$pids $!"
done <<EOF
$sat_pods
EOF
for pid in $pids; do
  wait "$pid"
done

fi # COZY_STORAGE_BACKEND

echo "[post-install-prep] applying StorageClasses"
render_linstor_storageclasses | $KUBECTL_BOUND kubectl apply --request-timeout=60s -f -

if [ "${COZY_LINSTOR_DRBD_ENABLED:-true}" = false ]; then
  wait_for_object 600 "CDI StorageProfile/local" storageprofile local
  echo "[post-install-prep] forcing CDI StorageProfile/local to RWO/Block"
  patch_local_cdi_storage_profile
fi

# The CRDs come from the metallb release, so its state is what separates CRDs
# that are late from an operator that never got as far as installing them.
if ! wait_for_object 300 "MetalLB CRDs" crd ipaddresspools.metallb.io l2advertisements.metallb.io; then
  timeout -k 5 30 kubectl get hr -n cozy-metallb \
    --request-timeout=10s 2>&1 | tail -n 30 >&2
  exit 1
fi

echo "[post-install-prep] applying MetalLB IPAddressPool"
$KUBECTL_BOUND kubectl apply --request-timeout=60s -f - <<'EOF'
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
