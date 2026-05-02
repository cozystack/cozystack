#!/usr/bin/env bash
# Pre-pull images to all cluster nodes.
#
# Reads image references from stdin, one per line. Empty lines and lines
# starting with '#' are ignored.
#
# Some workloads (OVN raft, LINSTOR) are sensitive to peer-readiness at
# startup: if nodes pull the image at different speeds, one replica boots
# before its peers are reachable, exhausts its raft connection retries, and
# the HelmRelease install times out. Pre-pulling eliminates the pull stagger
# so all replicas start within milliseconds of each other.
#
# Implementation: a DaemonSet with one regular container per image (parallel
# pulls — total time = max of any one image rather than sum). kubectl rollout
# status blocks until all pods are Ready (= all containers running = all
# images cached on every node), then we delete the DaemonSet.

set -euo pipefail

cleanup() {
  kubectl delete daemonset e2e-image-prepuller -n kube-system --ignore-not-found
}
trap cleanup EXIT

mapfile -t images < <(grep -Ev '^[[:space:]]*(#|$)' | sort -u)

if [[ ${#images[@]} -eq 0 ]]; then
  echo "e2e-prepull-images: no images on stdin, nothing to do" >&2
  exit 0
fi

echo "Pre-pulling ${#images[@]} image(s):"
printf '  %s\n' "${images[@]}"

containers=""
i=0
for image in "${images[@]}"; do
  containers+="
      - name: pull-${i}
        image: ${image}
        command: [\"sleep\", \"infinity\"]
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            cpu: 1m
            memory: 1Mi"
  i=$((i + 1))
done

kubectl apply -f - <<EOF
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
      # hostNetwork bypasses the CNI: this script runs BEFORE Cozystack
      # installs kube-ovn, so the cluster has no working CNI yet and a normal
      # pod would stay ContainerCreating with NetworkPluginNotReady, never
      # reaching image-pull. With hostNetwork the pod sandbox is created on
      # the host's namespace and the kubelet pulls images right away.
      # Same pattern the cozystack-operator pod uses for the same reason.
      hostNetwork: true
      # No need for an SA token — this pod just sleeps while images pull.
      automountServiceAccountToken: false
      tolerations:
      # Reach every node including control-plane and nodes still coming up.
      - operator: Exists
      containers:${containers}
EOF

echo "Waiting for e2e-image-prepuller DaemonSet to become ready..."
kubectl rollout status daemonset/e2e-image-prepuller -n kube-system --timeout=10m

echo "Image pre-pull complete."
