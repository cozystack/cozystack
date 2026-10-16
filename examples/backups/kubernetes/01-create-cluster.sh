#!/bin/bash
# Step 01: Create a tenant Kubernetes cluster with one worker pool and seed it
# with a marker held in etcd (a ConfigMap) and on a PersistentVolume.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00-helpers.sh"

print_header "Step 01: Create Kubernetes '${CLUSTER_NAME}' and seed a marker"

kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${CLUSTER_NAME}
  namespace: ${NAMESPACE}
spec:
  storageClass: replicated
---
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
metadata:
  name: ${CLUSTER_NAME}-${POOL_NAME}
  namespace: ${NAMESPACE}
spec:
  cluster: ${CLUSTER_NAME}
  minReplicas: 1
  maxReplicas: 1
  instanceType: u1.medium
  diskSize: ${WORKER_DISK_SIZE}
EOF

wait_hr_ready "$RELEASE" 1200
log_substep "Waiting for the worker node to join..."
tenant_connect
kubectl_ok=0
for _ in $(seq 1 120); do
    if tkubectl wait --for=condition=Ready node --all --timeout=10s >/dev/null 2>&1 \
        && [[ -n "$(tkubectl get nodes -o name)" ]] \
        && [[ -n "$(tkubectl get storageclass -o name)" ]]; then
        kubectl_ok=1
        break
    fi
    sleep 15
done
(( kubectl_ok )) || { log_error "worker node or StorageClass did not appear"; exit 1; }

tkubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: demo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: marker
  namespace: demo
data:
  marker: ${MARKER_VALUE}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: data
  namespace: demo
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 1Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: writer
  namespace: demo
spec:
  replicas: 1
  selector:
    matchLabels: {app: writer}
  template:
    metadata:
      labels: {app: writer}
    spec:
      containers:
        - name: writer
          image: busybox:1.36
          command: ["sh", "-c", "[ -f /data/marker ] || echo -n ${MARKER_VALUE} > /data/marker; sync; sleep infinity"]
          volumeMounts:
            - {name: data, mountPath: /data}
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: data}
EOF

wait_tenant_marker 900
cluster_ca_fingerprint > "$SCRIPT_DIR/.ca-fingerprint"
log_success "cluster CA $(cat "$SCRIPT_DIR/.ca-fingerprint")"
tenant_disconnect

echo -e "\n${GREEN}${BOLD}Next:${NC} ./02-create-backupjob.sh"
