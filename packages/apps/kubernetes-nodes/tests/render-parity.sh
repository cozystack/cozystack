#!/usr/bin/env bash
# Golden-parity gate for the Kubernetes-app split (Phase 2).
#
# Proves that the kubernetes-nodes chart renders a worker pool byte-identically
# to what the monolithic kubernetes chart renders for the same nodeGroup — so
# adopting existing MachineDeployment/KubevirtMachineTemplate objects into the
# split release causes zero drift (the KubevirtMachineTemplate is content-hash
# named; any divergence would roll every live worker VM).
#
# Compares KubevirtMachineTemplate, MachineDeployment, MachineHealthCheck, the
# worker WorkloadMonitor, and the Talos machine config the talos-reconcile Job
# applies, across several pool shapes. Exits non-zero on any difference.
#
# Coverage note: the `instanceType`-sized branch cannot be exercised offline
# (the chart resolves the VirtualMachineClusterInstancetype via `lookup`, which
# returns nil under `helm template`), so every case here uses explicit
# `resources` sizing. The GPU, kernel-module and kubelet-reservation branches
# ARE offline renderable and are covered below.
#
# The machine config is compared because it is the one thing the two charts
# duplicate outright: each renders its own copy of the TalosConfigTemplate that
# the Job applies, from its own _helpers.tpl. Nothing else in CI holds the two
# copies together, and a divergence is close to invisible in production — the
# HelmRelease goes Ready either way and the worker just boots with a config
# nobody asked for. This check previously stopped at the four pool objects, on
# the reasoning that the Job's content-hash name makes a divergence visible
# separately; that only holds for a divergence in the Job spec of ONE chart
# over time, not for two charts disagreeing with each other.
#
# The Job itself cannot be diffed byte-for-byte: the two charts run under
# different release names, so the Job name, serviceAccountName and RELEASE env
# legitimately differ. The machine config can, because it refers to the release
# only through shell variables the Job expands at runtime (${RELEASE}, ${NS},
# ${TALOS_TOKEN}, ...) — the rendered text is release-name independent, so any
# difference in it is a real difference in what the worker will boot with.
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

extract_machineconfig() { # <file> <out>
  # Pull the TalosConfigTemplate manifest out of the talos-reconcile Job's shell
  # script: the lines between the `kubectl apply` heredoc opener and its
  # terminator. Slicing the heredoc rather than parsing the YAML keeps the
  # comparison on the exact bytes the Job feeds to kubectl, comments and
  # indentation included — the machine config is a `data: |` string, so a
  # reindentation there is a real change to the config the worker parses.
  yq eval-all 'select(.kind == "Job" and (.metadata.name | test("talos-reconcile"))) | .spec.template.spec.containers[0].command[2]' "$1" \
    | awk '/cat <<EOF \| kubectl apply -f -/{f=1;next} f && /^[[:space:]]*EOF[[:space:]]*$/{f=0} f' >"$2"
  # A silent empty extraction would make this check pass unconditionally.
  if [ ! -s "$2" ]; then
    echo "extract_machineconfig: no machine config found in $1 — the Job, its heredoc marker or the container layout changed" >&2
    return 1
  fi
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
  if ! extract_machineconfig "$WORK/parent-out.yaml" "$WORK/p-mc.txt" \
    || ! extract_machineconfig "$WORK/child-out.yaml" "$WORK/c-mc.txt"; then
    echo "FAIL  [${case}] MachineConfig(talos-reconcile) — extraction failed"
    return 1
  fi
  if diff -u "$WORK/p-mc.txt" "$WORK/c-mc.txt" >"$WORK/d.txt"; then
    echo "OK    [${case}] MachineConfig(talos-reconcile)"
  else
    echo "FAIL  [${case}] MachineConfig(talos-reconcile)"
    cat "$WORK/d.txt"
    rc=1
  fi
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

# --- Case: explicit kernelModules on a pool with no GPU (the field is generic,
#     nothing about it is NVIDIA-specific) ---
cat >"$WORK/case-kernelmodules.yaml" <<'EOF'
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
kernelModules:
  - name: br_netfilter
    parameters:
      - nf_conntrack_helper=0
  - name: dummy
kubelet: {}
EOF

# --- Case: GPU pool that opts out of kernel modules with an explicit empty list
#     (distinct from unset, which auto-selects the NVIDIA set) ---
cat >"$WORK/case-kernelmodules-optout.yaml" <<'EOF'
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
kernelModules: []
kubelet: {}
EOF

RC=0
for c in resources gpu kubelet kernelmodules kernelmodules-optout; do
  write_values "$WORK/case-${c}.yaml"
  diff_kinds "$c" || RC=1
done

if [ "$RC" -eq 0 ]; then
  echo "GOLDEN PARITY: all pool objects byte-identical across all cases"
fi
exit "$RC"
