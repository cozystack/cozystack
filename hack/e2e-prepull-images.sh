#!/usr/bin/env bash
# Pre-pull timing-sensitive platform images to all cluster nodes.
#
# Some workloads (OVN raft, LINSTOR controller) are sensitive to peer-readiness
# at startup: if nodes pull the image at different speeds, one replica boots
# before its peers are reachable, exhausts its raft connection retries, and the
# HelmRelease install times out. Pre-pulling to all nodes eliminates the pull
# stagger so all replicas start within milliseconds of each other.
#
# Add an entry whenever you find a new workload with peer-dependent startup.
# Update versions here when bumping the corresponding chart dependency.

set -euo pipefail

# ---------------------------------------------------------------------------
# Image list
# ---------------------------------------------------------------------------
# Format: <registry>/<repo>:<tag>[@sha256:<digest>]
# Each image is pulled as an init container (imagePullPolicy=IfNotPresent),
# so this is a no-op if the image is already cached on that node.
#
# Source of each image and version is noted in the comment above it.

read -r -d '' PREPULL_YAML <<'YAML' || true
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: e2e-image-prepuller
  namespace: kube-system
  labels:
    app: e2e-image-prepuller
spec:
  selector:
    matchLabels:
      app: e2e-image-prepuller
  template:
    metadata:
      labels:
        app: e2e-image-prepuller
    spec:
      tolerations:
      # Tolerate all taints so the DaemonSet reaches every node including
      # control-plane nodes and nodes still coming up.
      - operator: Exists
      initContainers:
      # kube-ovn: OVN raft cluster, 3 ovn-central replicas must elect a leader.
      # Staggered pulls cause "failed to connect to peer" and raft timeout.
      # Source: packages/system/kubeovn/charts/kube-ovn/values.yaml
      - name: pull-kubeovn
        image: docker.io/kubeovn/kube-ovn:v1.15.10
        command: ['sh', '-c', 'exit 0']
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 1m
            memory: 1Mi
      # piraeus-server: LINSTOR controller/satellite cluster.
      # Satellites must register with the controller before storage is usable.
      # Source: packages/system/linstor/values.yaml (.piraeusServer.image)
      - name: pull-piraeus-server
        image: ghcr.io/cozystack/cozystack/piraeus-server:1.33.2@sha256:553f313ab35dc2e345ef3683156d29e75c23177e2750e9af3a83aa9e23941cbb
        command: ['sh', '-c', 'exit 0']
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 1m
            memory: 1Mi
      # linstor-csi: CSI driver sidecar shipped on every storage node.
      # Large-ish image; pre-pulling avoids a slow first-mount on the first PVC.
      # Source: packages/system/linstor/values.yaml (.linstorCSI.image)
      - name: pull-linstor-csi
        image: ghcr.io/cozystack/cozystack/linstor-csi:v1.10.5@sha256:b8f59b5659fb1791cb764d3f37df4cf29920aadcc10637231ba7d857233f377d
        command: ['sh', '-c', 'exit 0']
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 1m
            memory: 1Mi
      containers:
      # Pause container keeps the pod Running after init containers complete.
      # Running state is our signal that all images have been pulled on this node.
      - name: pause
        image: registry.k8s.io/pause:3.10
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 1m
            memory: 1Mi
YAML

kubectl apply -f - <<<"${PREPULL_YAML}"

node_count=$(kubectl get nodes --no-headers 2>/dev/null | wc -l)
echo "Waiting for e2e-image-prepuller on ${node_count} nodes (timeout 5m)..."

# A pod is Running when all init containers have exited 0 and the main
# container has started. That means every image in the init list was pulled.
timeout 300 sh -ec "
  until [ \$(kubectl get pods -n kube-system \
    -l app=e2e-image-prepuller \
    --field-selector=status.phase=Running \
    --no-headers 2>/dev/null | wc -l) -ge ${node_count} ]; do
    sleep 3
  done
"

kubectl delete daemonset e2e-image-prepuller -n kube-system --ignore-not-found
echo "Image pre-pull complete on all ${node_count} nodes."
