# Proxmox CSI Setup Guide

## Overview

Proxmox CSI driver provides persistent storage for Kubernetes using Proxmox storage backends.

## Prerequisites

- Kubernetes cluster with Proxmox nodes
- Proxmox VE server with storage backends
- Access to Proxmox API

## Step 1: Install Proxmox CSI Driver

1. Create namespace:
```bash
kubectl create namespace proxmox-csi-system
```

2. Install CSI driver:
```bash
kubectl apply -f https://raw.githubusercontent.com/prometheus-operator/prometheus-operator/main/bundle.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-provisioner/master/deploy/kubernetes/rbac.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-csi/external-snapshotter/master/deploy/kubernetes/rbac.yaml
```

## Step 2: Configure Proxmox CSI

1. Create CSI driver configuration:
```yaml
# proxmox-csi-config.yaml
apiVersion: v1
kind: Secret
metadata:
  name: proxmox-csi-credentials
  namespace: proxmox-csi-system
type: Opaque
stringData:
  username: ${PROXMOX_USERNAME}
  password: ${PROXMOX_PASSWORD}
  url: ${PROXMOX_URL}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: proxmox-csi-config
  namespace: proxmox-csi-system
data:
  config.yaml: |
    proxmox:
      server: ${PROXMOX_SERVER}
      insecure: false
      storage:
        default: local-lvm
        pools:
          - name: local-lvm
            type: lvm
            path: /dev/pve
          - name: local-zfs
            type: zfs
            path: rpool
```

2. Apply configuration:
```bash
kubectl apply -f proxmox-csi-config.yaml
```

## Step 3: Deploy CSI Driver

1. Create CSI driver deployment:
```yaml
# proxmox-csi-driver.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: proxmox-csi-driver
  namespace: proxmox-csi-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: proxmox-csi-driver
  template:
    metadata:
      labels:
        app: proxmox-csi-driver
    spec:
      serviceAccountName: proxmox-csi-driver
      containers:
      - name: csi-driver
        image: proxmox/csi-driver:latest
        args:
        - --endpoint=$(CSI_ENDPOINT)
        - --logtostderr
        - --v=2
        env:
        - name: CSI_ENDPOINT
          value: unix:///var/lib/csi/sockets/pluginproxy/csi.sock
        - name: PROXMOX_CONFIG
          value: /etc/proxmox/config.yaml
        volumeMounts:
        - name: socket-dir
          mountPath: /var/lib/csi/sockets/pluginproxy/
        - name: config
          mountPath: /etc/proxmox
        ports:
        - name: healthz
          containerPort: 9808
          protocol: TCP
        livenessProbe:
          httpGet:
            path: /healthz
            port: healthz
          initialDelaySeconds: 10
          timeoutSeconds: 3
          periodSeconds: 10
          failureThreshold: 5
      volumes:
      - name: socket-dir
        emptyDir: {}
      - name: config
        configMap:
          name: proxmox-csi-config
---
apiVersion: v1
kind: Service
metadata:
  name: proxmox-csi-driver
  namespace: proxmox-csi-system
spec:
  selector:
    app: proxmox-csi-driver
  ports:
  - name: healthz
    port: 9808
    targetPort: healthz
    protocol: TCP
```

2. Apply CSI driver:
```bash
kubectl apply -f proxmox-csi-driver.yaml
```

## Step 4: Create Storage Classes

1. Create storage classes:
```yaml
# storage-classes.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: proxmox-lvm
provisioner: proxmox.csi.k8s.io
parameters:
  storage: local-lvm
  type: lvm
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: WaitForFirstConsumer
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: proxmox-zfs
provisioner: proxmox.csi.k8s.io
parameters:
  storage: local-zfs
  type: zfs
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: WaitForFirstConsumer
```

2. Apply storage classes:
```bash
kubectl apply -f storage-classes.yaml
```

## Step 5: Test CSI Driver

1. Create test PVC:
```yaml
# test-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: proxmox-lvm
---
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test-container
    image: busybox
    command: ['sh', '-c', 'sleep 3600']
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: test-pvc
```

2. Apply test resources:
```bash
kubectl apply -f test-pvc.yaml
```

3. Verify PVC and PV:
```bash
kubectl get pvc
kubectl get pv
kubectl describe pvc test-pvc
```

## Step 6: Configure Volume Snapshots

1. Create snapshot class:
```yaml
# snapshot-class.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: proxmox-snapshot-class
driver: proxmox.csi.k8s.io
deletionPolicy: Delete
```

2. Apply snapshot class:
```bash
kubectl apply -f snapshot-class.yaml
```

## Troubleshooting

### Common Issues

1. **CSI Driver Not Starting**:
   - Check Proxmox credentials
   - Verify storage backend configuration
   - Review driver logs

2. **Volume Provisioning Fails**:
   - Check storage class parameters
   - Verify Proxmox storage availability
   - Review provisioner logs

3. **Volume Mount Fails**:
   - Check node compatibility
   - Verify volume attachment
   - Review kubelet logs

### Debug Commands

```bash
# Check CSI driver status
kubectl get pods -n proxmox-csi-system

# Check CSI driver logs
kubectl logs -n proxmox-csi-system -l app=proxmox-csi-driver

# Check storage classes
kubectl get storageclass

# Check PVC status
kubectl get pvc -o wide

# Check PV status
kubectl get pv -o wide
```
