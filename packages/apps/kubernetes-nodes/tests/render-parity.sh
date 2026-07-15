#!/usr/bin/env bash
# Golden-parity gate for the Kubernetes-app split (Phase 2).
#
# Proves that the kubernetes-nodes chart renders a worker pool byte-identically
# to what the monolithic kubernetes chart renders for the same nodeGroup — so
# adopting existing MachineDeployment/KubevirtMachineTemplate objects into the
# split release causes zero drift (the KubevirtMachineTemplate is content-hash
# named; any divergence would roll every live worker VM).
#
# Compares KubevirtMachineTemplate, MachineDeployment, MachineHealthCheck and
# the worker WorkloadMonitor. Exits non-zero on any difference.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PARENT="${SCRIPT_DIR}/../../kubernetes"
CHILD="${SCRIPT_DIR}/.."
NS=tenant-test
CLUSTER=myk8s
POOL=md0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

cat >"$WORK/parent.yaml" <<EOF
_namespace:
  etcd: tenant-test
  ingress: ""
  host: ""
  monitoring: ""
  seaweedfs: ""
_cluster:
  cluster-domain: cozy.local
storageClass: replicated
version: "v1.35"
nodeGroups:
  ${POOL}:
    minReplicas: 0
    maxReplicas: 3
    instanceType: ""
    diskSize: 20Gi
    storageClass: replicated
    roles: [ingress-nginx]
    resources:
      cpu: "2"
      memory: 4Gi
    gpus: []
    kubelet: {}
EOF

cat >"$WORK/child.yaml" <<EOF
cluster: ${CLUSTER}
_cluster:
  cluster-domain: cozy.local
storageClass: replicated
version: "v1.35"
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
roles: [ingress-nginx]
resources:
  cpu: "2"
  memory: 4Gi
gpus: []
kubelet: {}
maxUnhealthy: "50%"
nodeStartupTimeout: "10m"
EOF

helm template "kubernetes-${CLUSTER}" "$PARENT" -n "$NS" -f "$WORK/parent.yaml" >"$WORK/parent-out.yaml"
helm template "kubernetes-nodes-${CLUSTER}-${POOL}" "$CHILD" -n "$NS" -f "$WORK/child.yaml" >"$WORK/child-out.yaml"

extract() { # <file> <yq-select-expr> <out>
  # Strip helm's `# Source: <chart>/templates/...` provenance comment — it names
  # the rendering template file, which legitimately differs (kubernetes vs
  # kubernetes-nodes) and is not part of the applied object.
  yq eval-all "select($2)" "$1" | sed '/^# Source:/d' >"$3"
}

rc=0
for spec in \
  'KubevirtMachineTemplate|.kind == "KubevirtMachineTemplate"' \
  'MachineDeployment|.kind == "MachineDeployment"' \
  'MachineHealthCheck|.kind == "MachineHealthCheck"' \
  'WorkloadMonitor(worker)|.kind == "WorkloadMonitor" and .spec.type == "worker"' \
; do
  label="${spec%%|}"; label="${spec%%|*}"; sel="${spec#*|}"
  extract "$WORK/parent-out.yaml" "$sel" "$WORK/p.yaml"
  extract "$WORK/child-out.yaml" "$sel" "$WORK/c.yaml"
  if diff -u "$WORK/p.yaml" "$WORK/c.yaml" >"$WORK/d.txt"; then
    echo "OK    parity: ${label}"
  else
    echo "FAIL  parity: ${label}"
    cat "$WORK/d.txt"
    rc=1
  fi
done

if [ "$rc" -eq 0 ]; then
  echo "GOLDEN PARITY: all pool objects byte-identical between kubernetes and kubernetes-nodes"
fi
exit "$rc"
