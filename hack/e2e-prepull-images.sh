#!/usr/bin/env bash
# Pre-pull images to all cluster nodes.
#
# Reads image references from stdin, one per line. Empty lines and lines
# starting with '#' are ignored.
#
# Some workloads (OVN raft, LINSTOR, cert-manager) are sensitive to slow or
# staggered pulls at install time: a HelmRelease can time out while one node
# is still pulling. Pre-pulling eliminates the stagger.
#
# Distroless images (cert-manager, etc.) ship no shell or `sleep`, so we
# can't just `command: ["sleep", "infinity"]`. Trick: an initContainer copies
# busybox (a static binary) into a shared emptyDir, then every prepull
# container execs /shared/sleep — the kernel execve's the binary from the
# volume, the container's filesystem doesn't need to provide it.

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
        command: [\"/shared/sleep\", \"infinity\"]
        imagePullPolicy: IfNotPresent
        volumeMounts:
        - name: bin
          mountPath: /shared
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
      # installs kube-ovn, so a normal pod would stay ContainerCreating with
      # NetworkPluginNotReady.
      hostNetwork: true
      automountServiceAccountToken: false
      tolerations:
      - operator: Exists
      initContainers:
      # Stage a static \`sleep\` into the shared volume so distroless images
      # (no shell, no /bin/sleep) can still keep their container alive.
      # musl tag = statically linked; the glibc default needs an ELF
      # interpreter that distroless images don't ship, so execve fails with
      # "no such file or directory" even though the binary is right there.
      - name: stage-sleep
        image: busybox:musl
        command: ["cp", "/bin/busybox", "/shared/sleep"]
        volumeMounts:
        - name: bin
          mountPath: /shared
      volumes:
      - name: bin
        emptyDir: {}
      containers:${containers}
EOF

echo "Waiting for e2e-image-prepuller DaemonSet to become ready..."
kubectl rollout status daemonset/e2e-image-prepuller -n kube-system --timeout=10m

echo "Image pre-pull complete."
