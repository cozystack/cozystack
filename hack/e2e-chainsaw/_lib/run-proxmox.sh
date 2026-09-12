#!/bin/sh
# Imperative half of the kubernetes-proxmox suite.
#
# Chainsaw's declarative asserts cover everything that is a Kubernetes object in
# THIS cluster, and the suite uses them for exactly that. What is left needs a
# shell: the hypervisor is reached over SSH and answers in `qm` output, and the
# tenant cluster is reached with a kubeconfig that only exists once its control
# plane is up. Those are the two things below.
#
# Deliberately not a port of _lib/run-kubernetes.sh. That script's diagnostic
# half is virt-launcher Pods, VirtualMachineInstances and guest consoles —
# objects with no counterpart on an external hypervisor — so sharing it would
# mean carrying a thousand lines that can never fire here.
#
# Every subcommand exits non-zero on failure and says which check failed. There
# is no `|| echo` anywhere in this file on purpose: the pattern turns a failed
# command into a green run, and this repository has been bitten by it.
set -eu

NS="${COZY_PROXMOX_NS:-tenant-e2eproxmox}"
CLUSTER="${COZY_PROXMOX_CLUSTER:-k8s-e2e}"
PVE_SSH="${COZY_PVE_SSH:?COZY_PVE_SSH is required, e.g. root@192.168.20.1}"
PVE_SSH_KEY="${COZY_PVE_SSH_KEY:-$HOME/.ssh/id_ed25519}"
STORAGE="${COZY_PROXMOX_STORAGE:?COZY_PROXMOX_STORAGE is required, the PVE storage id for the PVC probe}"
TENANT_KUBECONFIG="${TMPDIR:-/tmp}/proxmox-e2e-tenant.kubeconfig"

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
die() { printf '[%s] FAILED: %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; exit 1; }

pve() { ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new -i "$PVE_SSH_KEY" "$PVE_SSH" "$@"; }

# The Machine carries the VM id in the ProxmoxMachine it references. Reading it
# from the cluster rather than guessing keeps this working when a replacement
# lands on a different id.
vmid_of_machine() {
  kubectl -n "$NS" get proxmoxmachine -o jsonpath='{.items[0].spec.virtualMachineID}'
}

# --- checks -----------------------------------------------------------------

# A full clone is a copy of the template's disk; a linked clone references it and
# stores only the increment. Nothing fails when this is wrong — the pool comes
# up either way — so the only place it can be caught is here, by the shape of the
# volume name: a linked clone points at `base-<template>-disk-N/`.
check_linked_clone() {
  vmid=$(vmid_of_machine)
  [ -n "$vmid" ] || die "no ProxmoxMachine with a virtualMachineID in $NS"
  log "VM $vmid: checking the boot disk is a linked clone"

  bootdisk=$(pve "qm config $vmid" | sed -n 's/^scsi0: *//p')
  [ -n "$bootdisk" ] || die "VM $vmid has no scsi0"

  case "$bootdisk" in
    *:base-*-disk-*/*) log "linked clone confirmed: $bootdisk" ;;
    *) die "VM $vmid boot disk is a full copy, not a linked clone: $bootdisk" ;;
  esac
}

# The tenant kubeconfig exists only after Kamaji has issued it. Fetching it is
# also the first proof that the control plane came up at all.
fetch_tenant_kubeconfig() {
  kubectl -n "$NS" get secret "${CLUSTER}-admin-kubeconfig" \
    -o jsonpath='{.data.super-admin\.svc}' | base64 -d > "$TENANT_KUBECONFIG"
  [ -s "$TENANT_KUBECONFIG" ] || die "tenant kubeconfig is empty — did the control plane come up?"
}

# providerID and the topology labels are applied by the cloud-controller-manager,
# and the CSI node plugin refuses to start without the labels. Asserting them
# here is what separates "the node joined" from "the node is usable".
check_tenant_node() {
  fetch_tenant_kubeconfig
  log "checking the tenant node is Ready and initialised by the CCM"

  node=$(KUBECONFIG="$TENANT_KUBECONFIG" kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
  [ -n "$node" ] || die "tenant cluster has no nodes"

  provider=$(KUBECONFIG="$TENANT_KUBECONFIG" kubectl get node "$node" -o jsonpath='{.spec.providerID}')
  region=$(KUBECONFIG="$TENANT_KUBECONFIG" kubectl get node "$node" \
    -o jsonpath='{.metadata.labels.topology\.kubernetes\.io/region}')

  [ -n "$provider" ] || die "node $node has no providerID — the CCM did not initialise it"
  [ -n "$region" ] || die "node $node has no topology region label — the CSI node plugin cannot start"
  log "node $node: providerID=$provider region=$region"
}

# Bound is not proof: a StorageClass can bind a volume that never becomes a disk.
# The disk on the hypervisor is the proof, so the check reads `qm config` and
# then removes what it created.
check_pvc_round_trip() {
  fetch_tenant_kubeconfig
  vmid=$(vmid_of_machine)
  log "binding a PVC and looking for its disk on VM $vmid"

  KUBECONFIG="$TENANT_KUBECONFIG" kubectl apply -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: e2e-csi-probe
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: proxmox
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: e2e-csi-probe
spec:
  containers:
  - name: c
    image: busybox:1.36
    command: ["sh", "-c", "sleep 600"]
    volumeMounts:
    - name: v
      mountPath: /data
  volumes:
  - name: v
    persistentVolumeClaim:
      claimName: e2e-csi-probe
EOF

  KUBECONFIG="$TENANT_KUBECONFIG" kubectl wait --for=condition=Ready pod/e2e-csi-probe --timeout=5m \
    || die "the probe Pod never became Ready — the volume did not attach"

  if ! pve "qm config $vmid" | grep -q "^scsi[1-9].*$STORAGE:"; then
    pve "qm config $vmid" | sed 's/^/  qm-config: /' >&2
    die "PVC bound but no disk is attached to VM $vmid on storage $STORAGE"
  fi
  log "disk attached on the hypervisor"

  KUBECONFIG="$TENANT_KUBECONFIG" kubectl delete pod e2e-csi-probe --wait=true
  KUBECONFIG="$TENANT_KUBECONFIG" kubectl delete pvc e2e-csi-probe --wait=true
}

# The regression this exists for: with a blanket toleration the CCM Pod outlived
# the Node it ran on, and since that controller is what removes a departed Node,
# a replacement deadlocked until an operator deleted the Node by hand. "Without
# intervention" is the whole assertion, so nothing here touches the cluster
# beyond the initial delete.
check_replacement_converges() {
  deadline=$(( $(date +%s) + 900 ))
  old=$(kubectl -n "$NS" get machine -o jsonpath='{.items[0].metadata.name}')
  [ -n "$old" ] || die "no Machine to replace in $NS"
  log "deleting Machine $old and waiting for an unattended replacement"

  kubectl -n "$NS" delete machine "$old" --wait=false

  while [ "$(date +%s)" -lt "$deadline" ]; do
    name=$(kubectl -n "$NS" get machine -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
    phase=$(kubectl -n "$NS" get machine -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
    noderef=$(kubectl -n "$NS" get machine -o jsonpath='{.items[0].status.nodeRef.name}' 2>/dev/null || true)
    if [ -n "$name" ] && [ "$name" != "$old" ] && [ "$phase" = "Running" ] && [ -n "$noderef" ]; then
      log "replacement $name reached Running with nodeRef $noderef"
      return 0
    fi
    sleep 15
  done

  kubectl -n "$NS" get machine,machinedeployment -o wide >&2 || true
  die "no replacement reached Running with a nodeRef within 15m — the CCM Pod is probably stranded on the departed Node"
}

# Runs from the suite's catch. Cheap reads first, so a truncated dump still
# carries the ones that discriminate between failure modes.
dump_diagnostics() {
  log "=== Machines and their infrastructure ==="
  kubectl -n "$NS" get machine,proxmoxmachine,machinedeployment,proxmoxcluster -o wide || true
  log "=== capmox controller ==="
  kubectl -n cozy-cluster-api logs -l control-plane=controller-manager --tail=80 \
    --selector=cluster.x-k8s.io/provider=infrastructure-proxmox || true
  log "=== VMs on the hypervisor ==="
  pve "qm list" || true
  if [ -s "$TENANT_KUBECONFIG" ]; then
    log "=== tenant nodes and pods ==="
    KUBECONFIG="$TENANT_KUBECONFIG" kubectl get nodes -o wide || true
    KUBECONFIG="$TENANT_KUBECONFIG" kubectl get pods -A -o wide || true
  fi
}

case "${1:-}" in
  linked-clone)   check_linked_clone ;;
  tenant-node)    check_tenant_node ;;
  pvc-round-trip) check_pvc_round_trip ;;
  replacement)    check_replacement_converges ;;
  diagnostics)    dump_diagnostics ;;
  *) die "usage: $0 {linked-clone|tenant-node|pvc-round-trip|replacement|diagnostics}" ;;
esac
