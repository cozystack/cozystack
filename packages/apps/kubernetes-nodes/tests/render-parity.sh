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
# the worker WorkloadMonitor across several pool shapes. Exits non-zero on any
# difference.
#
# Coverage note: the `instanceType`-sized branch cannot be exercised offline
# (the chart resolves the VirtualMachineClusterInstancetype via `lookup`, which
# returns nil under `helm template`), so every case here uses explicit
# `resources` sizing. The GPU and kubelet-reservation branches ARE offline
# renderable and are covered below. Scope: only the four pool objects are
# compared; the talos-reconcile Job's rendered output is not (its own
# content-hash name makes a divergence there visible separately).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PARENT="${SCRIPT_DIR}/../../kubernetes"
CHILD="${SCRIPT_DIR}/.."
NS=tenant-test
CLUSTER=myk8s
POOL=md0
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Shared, case-invariant inputs. The parent's default nodeHealthCheck
# (maxUnhealthy/nodeStartupTimeout) equals the child's per-pool defaults set
# here, so the MachineHealthCheck stays identical without extra parent config.
write_values() { # <pool-fields-file>
  local pf="$1"
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
EOF
  sed 's/^/    /' "$pf" >>"$WORK/parent.yaml"

  # Child takes the pool fields flat at the top level; storageClass comes from
  # the pool fields (do not repeat it in the header — avoids a duplicate key).
  cat >"$WORK/child.yaml" <<EOF
cluster: ${CLUSTER}
_cluster:
  cluster-domain: cozy.local
version: "v1.35"
maxUnhealthy: "50%"
nodeStartupTimeout: "10m"
EOF
  cat "$pf" >>"$WORK/child.yaml"
}

extract() { # <file> <yq-select-expr> <out>
  # Strip helm's `# Source: <chart>/templates/...` provenance comment — it names
  # the rendering template file, which legitimately differs (kubernetes vs
  # kubernetes-nodes) and is not part of the applied object.
  yq eval-all "select($2)" "$1" | sed '/^# Source:/d' >"$3"
}

diff_kinds() { # <case-name>
  local case="$1" rc=0
  helm template "kubernetes-${CLUSTER}" "$PARENT" -n "$NS" -f "$WORK/parent.yaml" >"$WORK/parent-out.yaml"
  helm template "kubernetes-nodes-${CLUSTER}-${POOL}" "$CHILD" -n "$NS" -f "$WORK/child.yaml" >"$WORK/child-out.yaml"
  for spec in \
    'KubevirtMachineTemplate|.kind == "KubevirtMachineTemplate"' \
    'MachineDeployment|.kind == "MachineDeployment"' \
    'MachineHealthCheck|.kind == "MachineHealthCheck"' \
    'WorkloadMonitor(worker)|.kind == "WorkloadMonitor" and .spec.type == "worker"' \
  ; do
    local label="${spec%%|*}" sel="${spec#*|}"
    extract "$WORK/parent-out.yaml" "$sel" "$WORK/p.yaml"
    extract "$WORK/child-out.yaml" "$sel" "$WORK/c.yaml"
    if diff -u "$WORK/p.yaml" "$WORK/c.yaml" >"$WORK/d.txt"; then
      echo "OK    [${case}] ${label}"
    else
      echo "FAIL  [${case}] ${label}"
      cat "$WORK/d.txt"
      rc=1
    fi
  done
  return "$rc"
}

# --- Case: resources-sized (no GPU, no kubelet overrides) ---
cat >"$WORK/case-resources.yaml" <<'EOF'
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

# --- Case: GPU pool (resources-sized so the instanceType lookup is not hit;
#     NVIDIA needs >= 4 GiB RAM) ---
cat >"$WORK/case-gpu.yaml" <<'EOF'
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: []
resources:
  cpu: "4"
  memory: 8Gi
gpus:
  - name: nvidia.com/AD102GL_L40S
kubelet: {}
EOF

# --- Case: kubelet reservation overrides ---
cat >"$WORK/case-kubelet.yaml" <<'EOF'
minReplicas: 0
maxReplicas: 3
instanceType: ""
diskSize: 20Gi
storageClass: replicated
roles: []
resources:
  cpu: "2"
  memory: 4Gi
gpus: []
kubelet:
  systemReservedMemory: 512Mi
  kubeReservedMemory: 512Mi
  systemReservedCpu: 100m
  kubeReservedCpu: 100m
  evictionHardMemory: 5%
  evictionSoftMemory: 8%
EOF

RC=0
for c in resources gpu kubelet; do
  write_values "$WORK/case-${c}.yaml"
  diff_kinds "$c" || RC=1
done

if [ "$RC" -eq 0 ]; then
  echo "GOLDEN PARITY: all pool objects byte-identical across all cases"
fi
exit "$RC"
