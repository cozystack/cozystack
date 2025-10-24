# Fresh CozyStack v0.37.2 Installation Plan

**Date**: 2025-10-24  
**Approach**: Option C (Fresh Install + Proxmox Bundle)  
**Target Version**: v0.37.2  
**Bundle**: paas-proxmox

## Overview

Install clean CozyStack v0.37.2 cluster with paas-proxmox bundle for Proxmox VE integration.

**Timeline**: 2 weeks total
- Week 1: Installation and configuration (Days 1-5)
- Week 2: Proxmox integration testing (Days 6-10)
- Week 3+: Migration planning and execution

## Prerequisites

### Proxmox Resources Needed

**Control Plane VMs** (3 VMs):
```
Name: cozy-new-cp1, cozy-new-cp2, cozy-new-cp3
CPU: 4 cores each
RAM: 8GB each
Disk: 50GB each (system) + 100GB (LINSTOR)
Network: vmbr0
Template: Talos Linux or Ubuntu 22.04
```

**Worker VM** (1 VM, optional if using Proxmox host):
```
Name: cozy-new-worker1
CPU: 8 cores
RAM: 16GB
Disk: 100GB (system) + 200GB (LINSTOR)
Network: vmbr0
Template: Talos Linux or Ubuntu 22.04
```

**Total Resources**:
- VMs: 4
- CPUs: 20 cores
- RAM: 40GB
- Disk: 550GB

### Network Requirements

- **IP Range**: 10.0.0.200-220 (or different from old cluster)
- **Gateway**: 10.0.0.1
- **DNS**: 10.0.0.1
- **VLAN**: Same as old cluster or dedicated

### Software Requirements

- Talos Linux image or Ubuntu 22.04 cloud image
- kubectl 1.28+
- helm 3.12+
- talosctl (if using Talos)

## Installation Steps

### Week 1: Installation

#### Day 1: VM Preparation (4 hours)

**Step 1.1: Create VM Templates**

On Proxmox host:
```bash
# If using Talos Linux (recommended)
# Download Talos v1.8.0+ image
wget https://github.com/siderolabs/talos/releases/download/v1.8.0/metal-amd64.raw.xz
xz -d metal-amd64.raw.xz

# Create template VM
qm create 9100 --name talos-v1.8-template \
  --memory 8192 --cores 4 --net0 virtio,bridge=vmbr0

# Import disk
qm importdisk 9100 metal-amd64.raw local-lvm

# Attach disk
qm set 9100 --scsihw virtio-scsi-pci --scsi0 local-lvm:vm-9100-disk-0

# Set boot
qm set 9100 --boot c --bootdisk scsi0

# Add second disk for LINSTOR
qm set 9100 --scsi1 local-lvm:100

# Convert to template
qm template 9100
```

**Step 1.2: Clone VMs for Control Plane**

```bash
# Clone 3 control plane VMs
qm clone 9100 1001 --name cozy-new-cp1 --full 1
qm clone 9100 1002 --name cozy-new-cp2 --full 1
qm clone 9100 1003 --name cozy-new-cp3 --full 1

# Optional: Clone worker
qm clone 9100 1011 --name cozy-new-worker1 --full 1

# Configure resources (if needed)
for vm in 1001 1002 1003; do
  qm set $vm --memory 8192 --cores 4
done
```

**Step 1.3: Start VMs**

```bash
# Start control plane VMs
qm start 1001
qm start 1002
qm start 1003

# Get IPs (from Proxmox console or DHCP)
qm guest exec 1001 -- ip addr show | grep inet
# Repeat for 1002, 1003

# Document IPs
echo "cozy-new-cp1: 10.0.0.201" > /root/new-cluster-ips.txt
echo "cozy-new-cp2: 10.0.0.202" >> /root/new-cluster-ips.txt
echo "cozy-new-cp3: 10.0.0.203" >> /root/new-cluster-ips.txt
```

#### Day 2: Talos/Kubernetes Bootstrap (6 hours)

**Step 2.1: Generate Talos Configuration**

```bash
# Install talosctl
curl -sL https://talos.dev/install | sh

# Generate config
talosctl gen config cozy-new \
  https://10.0.0.201:6443 \
  --output-dir /root/cozy-new-config \
  --with-docs=false \
  --with-examples=false

# Patch configuration for 3-node HA
# (Edit controlplane.yaml, worker.yaml as needed)
```

**Step 2.2: Apply Configuration**

```bash
# Apply to all control plane nodes
talosctl apply-config --insecure \
  --nodes 10.0.0.201 \
  --file /root/cozy-new-config/controlplane.yaml

talosctl apply-config --insecure \
  --nodes 10.0.0.202 \
  --file /root/cozy-new-config/controlplane.yaml

talosctl apply-config --insecure \
  --nodes 10.0.0.203 \
  --file /root/cozy-new-config/controlplane.yaml
```

**Step 2.3: Bootstrap Cluster**

```bash
# Bootstrap etcd on first node
talosctl bootstrap --nodes 10.0.0.201 \
  --endpoints 10.0.0.201 \
  --talosconfig /root/cozy-new-config/talosconfig

# Wait for cluster to form
sleep 120

# Get kubeconfig
talosctl kubeconfig --nodes 10.0.0.201 \
  --endpoints 10.0.0.201 \
  --talosconfig /root/cozy-new-config/talosconfig \
  /root/cozy-new-config/kubeconfig

# Verify cluster
export KUBECONFIG=/root/cozy-new-config/kubeconfig
kubectl get nodes
```

**Expected Result**:
```
NAME              STATUS   ROLES           AGE   VERSION
cozy-new-cp1      Ready    control-plane   2m    v1.32.x
cozy-new-cp2      Ready    control-plane   2m    v1.32.x
cozy-new-cp3      Ready    control-plane   2m    v1.32.x
```

#### Day 3: Install CozyStack v0.37.2 (6 hours)

**Step 3.1: Clone CozyStack Repository**

```bash
cd /root
git clone https://github.com/cozystack/cozystack.git cozystack-v0.37.2
cd cozystack-v0.37.2
git checkout v0.37.2

# Verify version
git describe --tags
# Should output: v0.37.2
```

**Step 3.2: Prepare Configuration**

```bash
# Create configuration
cat > /root/cozy-new-config/cozystack-values.yaml <<EOF
bundle: paas-proxmox

# Cluster configuration
cluster-domain: cozy-new.local
root-host: cozy-new.example.com

# Network configuration
ipv4-pod-cidr: 10.244.0.0/16
ipv4-pod-gateway: 10.244.0.1
ipv4-svc-cidr: 10.96.0.0/16
ipv4-join-cidr: 100.64.0.0/16

# API endpoint
api-server-endpoint: https://10.0.0.201:6443

# Disable telemetry if needed
telemetry-enabled: "false"
EOF
```

**Step 3.3: Run CozyStack Installer**

```bash
export KUBECONFIG=/root/cozy-new-config/kubeconfig

# Run installer with paas-proxmox bundle
cd /root/cozystack-v0.37.2

# Method 1: Using installer script
bash scripts/installer.sh \
  --bundle paas-proxmox \
  --config /root/cozy-new-config/cozystack-values.yaml

# OR Method 2: Using Helm directly
kubectl create namespace cozy-system

# Apply CozyStack ConfigMap
kubectl create configmap cozystack \
  --from-file=/root/cozy-new-config/cozystack-values.yaml \
  -n cozy-system

# Install platform chart
helm install cozystack-platform \
  packages/core/platform \
  --namespace cozy-system \
  --create-namespace \
  --set bundle=paas-proxmox \
  --timeout 30m \
  --wait
```

**Step 3.4: Monitor Installation**

```bash
# Watch HelmReleases
watch kubectl get hr -A

# Watch pods
watch kubectl get pods -A

# Check for any issues
kubectl get events -A --sort-by='.lastTimestamp' | tail -50
```

**Expected Duration**: 20-30 minutes for all components to be Ready

**Step 3.5: Verify Installation**

```bash
# Check all HelmReleases
kubectl get hr -A | grep -v True

# Should be empty (all True)

# Check pods
kubectl get pods -A | grep -v Running | grep -v Completed

# Should be minimal or none

# Verify CozyStack components
kubectl get pods -n cozy-system
kubectl get pods -n cozy-cilium
kubectl get pods -n cozy-kubeovn
```

#### Day 4: Configure Proxmox Integration (4 hours)

**Step 4.1: Verify paas-proxmox Bundle Components**

```bash
export KUBECONFIG=/root/cozy-new-config/kubeconfig

# Check installed components
kubectl get hr -A | grep -E 'proxmox|capi|kamaji|linstor'

# Expected components:
# - proxmox-csi (cozy-proxmox-csi namespace)
# - proxmox-ccm (cozy-proxmox-ccm namespace)
# - capi-operator (cozy-cluster-api namespace)
# - capi-providers (includes capmox)
# - kamaji (cozy-kamaji namespace)
# - linstor (cozy-linstor namespace)
# - metallb (cozy-metallb namespace)
```

**Step 4.2: Configure Proxmox Credentials**

```bash
# Create Proxmox API token on Proxmox host
# (if not already created)
pveum user token add capmox@pam capi --privsep 1

# Save token ID and secret
# Token ID: capmox@pam!capi
# Token Secret: <generated-secret>

# Create secret in Kubernetes
kubectl create secret generic proxmox-credentials \
  -n cozy-cluster-api \
  --from-literal=url=https://10.0.0.1:8006/api2/json \
  --from-literal=token_id=capmox@pam!capi \
  --from-literal=token_secret=<token-secret>
```

**Step 4.3: Configure Proxmox CSI**

```bash
# Create CSI configuration
cat > /tmp/proxmox-csi-values.yaml <<EOF
config:
  clusters:
    - url: https://10.0.0.1:8006/api2/json
      insecure: true
      token_id: "capmox@pam!csi"
      token_secret: "<csi-token-secret>"
      region: pve

storageClass:
  - name: proxmox-data-xfs
    storage: local-lvm
    reclaimPolicy: Delete
    fstype: xfs
  - name: proxmox-data-xfs-ssd
    storage: local-ssd
    reclaimPolicy: Delete
    fstype: xfs
EOF

# Apply configuration (if CSI chart supports values)
# Or configure via ConfigMap
```

**Step 4.4: Create ProxmoxCluster**

```bash
# Create ProxmoxCluster for new cluster
kubectl apply -f - <<EOF
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxCluster
metadata:
  name: cozy-new
  namespace: default
spec:
  allowedNodes:
    - pve
    - mgr
  controlPlaneEndpoint:
    host: 10.0.0.201
    port: 6443
  dnsServers:
    - 10.0.0.1
  ipv4Config:
    addresses:
      - 10.0.0.210-10.0.0.250
    gateway: 10.0.0.1
    prefix: 24
EOF

# Wait for ProxmoxCluster to be Ready
kubectl wait --for=condition=ready proxmoxcluster/cozy-new --timeout=300s
```

**Step 4.5: Verify Proxmox Components**

```bash
# Check capmox controller
kubectl get pods -n capmox-system

# Check Proxmox CSI
kubectl get pods -n cozy-proxmox-csi

# Check Proxmox CCM
kubectl get pods -n cozy-proxmox-ccm

# Check StorageClasses
kubectl get storageclass | grep proxmox

# Check ProxmoxCluster
kubectl get proxmoxclusters -A
```

#### Day 5: Validation (4 hours)

**Step 5.1: Health Check**

```bash
# All nodes Ready
kubectl get nodes

# All pods Running/Completed
kubectl get pods -A | grep -v Running | grep -v Completed

# All HelmReleases Ready
kubectl get hr -A | grep -v True

# All PVCs Bound (should be none yet)
kubectl get pvc -A
```

**Step 5.2: Basic Functionality Tests**

```bash
# Create test namespace
kubectl create namespace test-basic

# Test pod creation
kubectl run test-nginx --image=nginx -n test-basic
kubectl wait --for=condition=ready pod/test-nginx -n test-basic --timeout=60s

# Test service creation
kubectl expose pod test-nginx --port=80 -n test-basic

# Test storage
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
  namespace: test-basic
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: replicated
EOF

kubectl wait --for=condition=bound pvc/test-pvc -n test-basic --timeout=120s

# Cleanup
kubectl delete namespace test-basic
```

**Step 5.3: Document Installation**

```bash
# Save cluster state
kubectl get all,cm,secret,pvc,pv -A -o yaml > /root/cozy-new-config/initial-state.yaml

# Save versions
kubectl get pods -A -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].image}{"\n"}' > /root/cozy-new-config/component-versions.txt

# Verify version
kubectl get cm -n cozy-system cozystack -o yaml
```

### Week 2: Proxmox Integration Testing

#### Day 6-7: VM Creation Testing (8 hours)

**Test 1: Create Test ProxmoxMachine**

Already documented in previous test - but now on clean cluster:

```bash
# Create test VM via CAPI (with full workflow)
# This time use Kubernetes CRD if available
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: test-tenant
  namespace: default
spec:
  replicas: 1
  nodeGroups:
    - name: worker
      replicas: 1
      resources:
        cpu: 2
        memory: 4Gi
        disk: 20Gi
EOF

# Monitor tenant cluster creation
watch kubectl get kubernetes test-tenant

# Check if ProxmoxMachine created
watch kubectl get proxmoxmachines -A

# VERIFY IN PROXMOX (critical!)
ssh root@proxmox-host "qm list | grep test"

# Check VM boots and joins
kubectl get machines -A
```

**Test 2: Storage Provisioning**

```bash
# Test Proxmox CSI
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-proxmox-storage
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: proxmox-data-xfs
  resources:
    requests:
      storage: 10Gi
EOF

# Wait and verify
kubectl wait --for=condition=bound pvc/test-proxmox-storage --timeout=120s

# Check in Proxmox
ssh root@proxmox-host "pvesm list local-lvm"
```

**Test 3: Network Configuration**

```bash
# Test MetalLB
kubectl create deployment test-lb --image=nginx
kubectl expose deployment test-lb --port=80 --type=LoadBalancer

# Check external IP assigned
kubectl get svc test-lb

# Cleanup
kubectl delete deployment test-lb
kubectl delete svc test-lb
```

#### Day 8-9: Advanced Testing (8 hours)

**Test 4: Multi-Node Tenant Cluster**

```bash
# Create larger tenant cluster
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: prod-tenant
  namespace: tenant-prod
spec:
  replicas: 3
  nodeGroups:
    - name: worker
      replicas: 3
      resources:
        cpu: 4
        memory: 8Gi
        disk: 50Gi
EOF

# Monitor creation
watch kubectl get kubernetes prod-tenant -n tenant-prod

# VERIFY: 3 VMs created in Proxmox
ssh root@proxmox-host "qm list | grep prod-tenant"
```

**Test 5: Database Operator**

```bash
# Test PostgreSQL
kubectl create namespace db-test
kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: test-db
  namespace: db-test
spec:
  replicas: 2
  storage: 10Gi
EOF

# Verify
kubectl wait --for=condition=ready postgres/test-db -n db-test --timeout=300s
kubectl get pods -n db-test
```

**Test 6: Integration Tests**

```bash
# Run comprehensive integration checks
cd /root/cozystack-v0.37.2
./tests/proxmox-integration/extended-integrity-check.sh
```

#### Day 10: Documentation and Handoff (4 hours)

**Document New Cluster**:
```bash
# Create cluster documentation
cat > /root/NEW_CLUSTER_INFO.md <<EOF
# New CozyStack v0.37.2 Cluster

## Details
- Version: v0.37.2
- Bundle: paas-proxmox
- Installed: $(date)
- Nodes: 3 control plane + 1 worker

## Access
- Kubeconfig: /root/cozy-new-config/kubeconfig
- Talosconfig: /root/cozy-new-config/talosconfig

## IPs
- Control Plane 1: 10.0.0.201
- Control Plane 2: 10.0.0.202
- Control Plane 3: 10.0.0.203
- Worker 1: 10.0.0.211

## Proxmox Integration
- ProxmoxCluster: cozy-new (Ready)
- CSI Driver: Configured
- CCM: Running
- Templates: ID 9100 (Talos)

## Tests Passed
- ✅ VM creation via CAPI
- ✅ Storage provisioning
- ✅ Network configuration
- ✅ Tenant cluster creation
- ✅ Database operators
EOF
```

### Week 3+: Migration Planning

**Create Migration Strategy**:
```bash
# Inventory old cluster
export KUBECONFIG=/root/cozy/mgr-cozy/kubeconfig
kubectl get all -A > /root/old-cluster-inventory.txt

# Identify workloads to migrate
kubectl get deployments,statefulsets,daemonsets -A

# Plan migration sequence
# (To be detailed based on actual workloads)
```

## Quick Start Commands

### For Fresh Install

```bash
# 1. Prepare VMs in Proxmox
qm clone 9100 1001 --name cozy-new-cp1 --full 1
qm clone 9100 1002 --name cozy-new-cp2 --full 1
qm clone 9100 1003 --name cozy-new-cp3 --full 1
qm start 1001 && qm start 1002 && qm start 1003

# 2. Bootstrap Talos
talosctl gen config cozy-new https://10.0.0.201:6443 \
  --output-dir /root/cozy-new-config
# Apply to nodes...
talosctl bootstrap --nodes 10.0.0.201

# 3. Get kubeconfig
talosctl kubeconfig /root/cozy-new-config/kubeconfig

# 4. Install CozyStack
git clone https://github.com/cozystack/cozystack.git
cd cozystack
git checkout v0.37.2
bash scripts/installer.sh --bundle paas-proxmox

# 5. Verify
kubectl get hr -A
kubectl get pods -A
```

## Success Criteria

### Installation Complete When:

- ✅ All HelmReleases Ready
- ✅ All pods Running/Completed
- ✅ ProxmoxCluster configured and Ready
- ✅ capmox controller running
- ✅ Proxmox CSI/CCM operational
- ✅ Storage classes created
- ✅ Test VM created in Proxmox
- ✅ Basic functionality verified

### Integration Complete When:

- ✅ Tenant cluster created (VMs in Proxmox)
- ✅ Storage provisioning working
- ✅ Database operators functional
- ✅ Network connectivity verified
- ✅ All integrity checks pass

## Comparison: Old vs New

| Aspect | Old Cluster | New Cluster |
|--------|-------------|-------------|
| **Version** | v0.28.0 | v0.37.2 |
| **Age** | 219 days | Fresh |
| **Bundle** | paas-full | paas-proxmox |
| **Health** | Critical failures | Healthy |
| **VM Management** | KubeVirt (broken) | Proxmox CAPI |
| **Networking** | Broken (52+ days) | Clean |
| **Pods Failing** | 19+ | 0 |
| **Ready for Proxmox** | No | Yes |

## Rollback Plan

**If fresh install fails** (unlikely):
- Old cluster still running
- Can revert to old cluster
- No data lost
- Minimal impact

**Advantage of Option C**: Zero risk to existing cluster

## Timeline Summary

```
Week 1:
  Day 1: VM prep (4h)
  Day 2: K8s bootstrap (6h)
  Day 3: CozyStack install (6h)
  Day 4: Proxmox config (4h)
  Day 5: Validation (4h)
  Total: 24 hours

Week 2:
  Day 6-7: VM testing (8h)
  Day 8-9: Advanced testing (8h)
  Day 10: Documentation (4h)
  Total: 20 hours

Overall: 44 hours across 2 weeks
```

**Compared to Option A**: Would have been 30-44 hours with 30-40% success rate

## Next Immediate Steps

1. ✅ Document plan (this file)
2. ⏳ Create VMs in Proxmox
3. ⏳ Bootstrap Kubernetes
4. ⏳ Install CozyStack v0.37.2
5. ⏳ Test Proxmox integration

---

**Status**: READY TO EXECUTE  
**Next Action**: Create VMs in Proxmox  
**Estimated Start**: Today  
**Estimated Completion**: 2 weeks

