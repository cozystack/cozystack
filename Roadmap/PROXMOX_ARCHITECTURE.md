# Proxmox Integration Architecture

## Overview

This document describes the corrected architecture for Proxmox VE integration with CozyStack. The key principle is: **When using Proxmox as infrastructure, VMs are created directly in Proxmox via Cluster API Provider, NOT through KubeVirt.**

## Architecture Principles

### Wrong Approach ❌
```
User → Kubernetes API → KubeVirt → QEMU/KVM (in pods) → Storage
```

### Correct Approach ✅
```
User → Kubernetes API → Cluster API → Proxmox API → Proxmox VMs
                     ↓
                 Proxmox CSI → Proxmox Storage
                     ↓
                 Proxmox CCM → Proxmox Networking
```

## Component Stack

### 1. Infrastructure Layer (Proxmox VE)

```
Proxmox VE Host(s)
├── VM Management (via Proxmox API)
├── Storage Management (ZFS, LVM, Ceph, etc.)
├── Network Management (vmbr0, VLAN, etc.)
└── Resource Management (CPU, RAM, disk)
```

**Key Point**: Proxmox VE is the **hypervisor** and **VM lifecycle manager**.

### 2. Kubernetes Management Cluster

```
Kubernetes Cluster (CozyStack)
├── Control Plane (3 nodes)
│   └── Running on Proxmox VMs
├── Worker Nodes
│   └── Can run on Proxmox VMs OR Proxmox hosts (as VMs)
└── CozyStack Platform
    ├── Cluster API (CAPI)
    ├── Proxmox CSI Driver
    ├── Proxmox CCM
    └── Kamaji (Tenant clusters)
```

### 3. Integration Components

#### A. Cluster API Proxmox Provider (`capmox`)

**Purpose**: Manages VM lifecycle in Proxmox through Kubernetes API

**Resources**:
- `ProxmoxCluster` - Defines Proxmox cluster connection
- `ProxmoxMachine` - Defines individual VM specifications
- `ProxmoxMachineTemplate` - VM template for scaling

**Workflow**:
```
1. User creates Machine CRD
   ↓
2. CAPI creates ProxmoxMachine
   ↓
3. capmox provider calls Proxmox API
   ↓
4. Proxmox creates VM from template
   ↓
5. VM boots and joins Kubernetes
```

**Example**:
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: tenant-worker-1
spec:
  nodeName: pve           # Proxmox node
  template: ubuntu-22.04  # VM template
  cores: 4
  memory: 8192
  diskSize: 50
```

#### B. Proxmox CSI Driver

**Purpose**: Provides persistent storage for Kubernetes pods

**Storage Classes**:
- `proxmox-data-xfs` - Local XFS storage
- `proxmox-data-xfs-ssd` - SSD-backed storage
- `proxmox-zfs` - ZFS storage pool
- Custom storage backends

**Workflow**:
```
1. Pod requests PVC
   ↓
2. CSI driver calls Proxmox API
   ↓
3. Proxmox creates disk/volume
   ↓
4. CSI mounts volume to node
   ↓
5. Pod uses mounted volume
```

**Key**: Storage is created **in Proxmox**, not in Kubernetes.

#### C. Proxmox Cloud Controller Manager (CCM)

**Purpose**: Integrates Kubernetes with Proxmox infrastructure

**Features**:
- Node lifecycle management
- Node IP address assignment
- Node metadata sync
- (Optional) Load balancer integration

**Workflow**:
```
1. VM boots in Proxmox
   ↓
2. Kubelet starts on VM
   ↓
3. Node joins Kubernetes
   ↓
4. CCM detects new node
   ↓
5. CCM sets node labels/taints based on Proxmox metadata
```

#### D. LINSTOR (Hybrid Storage)

**Purpose**: Provides replicated storage across nodes

**Architecture**:
```
LINSTOR Controller (in Kubernetes)
    ↓
LINSTOR Satellites (on nodes)
    ↓
DRBD (block device replication)
    ↓
Underlying storage (Proxmox or local disks)
```

**Storage Classes**:
- `replicated` - Replicated storage (default 2 replicas)
- `linstor-thin-r1` - Thin-provisioned, 1 replica

#### E. Kamaji (Tenant Clusters)

**Purpose**: Creates tenant Kubernetes clusters using VMs

**Architecture**:
```
Management Cluster
├── Kamaji Control Plane
│   ├── Tenant API servers (pods)
│   ├── Tenant Controllers (pods)
│   └── Tenant etcd (pods)
└── CAPI creates tenant VMs in Proxmox
    ├── Worker VMs
    └── (Optional) Control plane VMs
```

## paas-proxmox Bundle

### Components Included

**Core Infrastructure**:
1. `fluxcd-operator` + `fluxcd` - GitOps
2. `cilium` - CNI (primary networking)
3. `kubeovn` - Advanced networking (VLAN, multi-tenant)

**CozyStack Platform**:
4. `cozy-proxy` - API proxy
5. `cert-manager` - Certificate management
6. `cozystack-api` - CozyStack API server
7. `cozystack-controller` - CozyStack controller
8. `cozystack-resource-definitions` - CRDs

**Monitoring & Observability**:
9. `victoria-metrics-operator` - Metrics
10. `monitoring` - Grafana, dashboards
11. `grafana-operator` - Grafana management

**Database Operators**:
12. `mariadb-operator` - MariaDB management
13. `postgres-operator` - PostgreSQL management
14. `rabbitmq-operator` - RabbitMQ management
15. `redis-operator` - Redis management

**Proxmox Integration** (KEY COMPONENTS):
16. `proxmox-csi` - Storage driver
17. `proxmox-ccm` - Cloud controller
18. `metallb` - Load balancer
19. `snapshot-controller` - Volume snapshots
20. `piraeus-operator` + `linstor` - Replicated storage

**Cluster API**:
21. `kamaji` - Tenant cluster management
22. `capi-operator` - Cluster API operator
23. `capi-providers` - Infrastructure providers (includes Proxmox)

**Additional**:
24. `telepresence` - Development tool
25. `dashboard` - Web UI

### Components NOT Included (vs paas-full)

**Removed (replaced by Proxmox)**:
- ❌ `kubevirt-operator` - Not needed, Proxmox manages VMs
- ❌ `kubevirt` - Not needed
- ❌ `kubevirt-instancetypes` - Not needed
- ❌ `kubevirt-cdi-operator` - Not needed
- ❌ `kubevirt-cdi` - Not needed

**Why**: KubeVirt creates VMs **inside Kubernetes pods** using QEMU/KVM. With Proxmox, VMs are created **directly in Proxmox hypervisor**, which is more efficient and leverages Proxmox's native VM management.

## VM Creation Workflows

### For Application Workloads (Tenant VMs)

**Method 1: Via Kubernetes App (Recommended)**

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: tenant-cluster
  namespace: tenant-demo
spec:
  replicas: 3
  nodeGroups:
    - name: worker
      replicas: 3
      # This triggers ProxmoxMachine creation
```

This creates a tenant Kubernetes cluster where:
- Control plane runs as **pods** (Kamaji)
- Worker nodes run as **Proxmox VMs** (CAPI + capmox)

**Method 2: Direct ProxmoxMachine (Advanced)**

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxCluster
    name: my-cluster
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxCluster
metadata:
  name: my-cluster
spec:
  server: proxmox.example.com
  controlPlaneEndpoint:
    host: 10.0.0.100
    port: 6443
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name: worker-1
spec:
  clusterName: my-cluster
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxMachine
    name: worker-1
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: worker-1
spec:
  nodeName: pve
  template: ubuntu-22.04-k8s
  cores: 4
  memory: 8192
  diskSize: 50
```

### For Database Services

**Option A: In LXC Containers** (Future, requires proxmox-lxcri)

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: my-db
spec:
  runtime: lxc  # Runs in Proxmox LXC
  replicas: 3
```

**Option B: In Proxmox VMs**

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: my-db
spec:
  runtime: vm  # Runs in Proxmox VM
  replicas: 3
```

**Option C: In Kubernetes Pods** (Default, current)

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: my-db
spec:
  # No runtime specified = pods
  replicas: 3
```

## Network Architecture

### Management Network

```
Proxmox Host(s)
├── vmbr0 (Bridge)
│   ├── Management VMs (Kubernetes control plane)
│   ├── Worker VMs
│   └── API access
└── vmbr1 (Optional, storage network)
    └── LINSTOR replication
```

### Pod Network (Inside Kubernetes)

```
Cilium (Primary CNI)
├── Pod-to-pod communication
├── Service routing
└── Network policies

Kube-OVN (Overlay)
├── VLAN support
├── Multi-tenant isolation
└── Advanced routing
```

### External Access

```
MetalLB (Load Balancer)
├── Assigns external IPs to Services
└── Integrates with Proxmox network
```

## Storage Architecture

### Hybrid Storage Model

```
Application Data
    ↓
Kubernetes PVC
    ↓
    ├─→ LINSTOR (replicated) → Local disks on nodes
    └─→ Proxmox CSI → Proxmox storage (ZFS, LVM, Ceph)
```

**Use Cases**:
- **LINSTOR**: Critical data, high availability, replication
- **Proxmox CSI**: Large volumes, VM disks, snapshots

## Security Considerations

### 1. API Access Control

- Proxmox API token with minimal permissions
- Kubernetes RBAC for CAPI resources
- Network policies for pod communication

### 2. Storage Security

- Encryption at rest (LUKS, ZFS encryption)
- Access control via Kubernetes RBAC
- Snapshot-based backups

### 3. Network Security

- VLAN isolation for tenants
- Cilium network policies
- Firewall rules on Proxmox

## Deployment Sequence

### 1. Proxmox Setup

```bash
1. Install Proxmox VE
2. Configure storage pools
3. Create VM templates
4. Setup networking (bridges, VLANs)
5. Create API token for CAPI
```

### 2. CozyStack Installation

```bash
1. Create initial VMs for Kubernetes control plane
2. Install Talos/Ubuntu on VMs
3. Bootstrap Kubernetes cluster
4. Install CozyStack with paas-proxmox bundle
5. Configure Proxmox credentials
```

### 3. Integration Verification

```bash
1. Verify CAPI provider is running
2. Check ProxmoxCluster is Ready
3. Test VM creation via CAPI
4. Verify CSI driver is working
5. Test storage provisioning
```

### 4. Tenant Cluster Creation

```bash
1. Create tenant namespace
2. Apply Kubernetes CRD
3. Wait for tenant cluster to provision
4. Access tenant cluster
```

## Comparison: KubeVirt vs Proxmox

### KubeVirt Approach (paas-full)

**Pros**:
- Everything managed by Kubernetes
- Portable across infrastructure
- Good for development/testing

**Cons**:
- VMs run in pods (overhead)
- Limited VM management features
- No native snapshot support
- Performance overhead

**Architecture**:
```
Pod → QEMU/KVM → VM → Guest OS
```

### Proxmox Approach (paas-proxmox)

**Pros**:
- Native VM management
- Better performance
- Rich feature set (snapshots, migration, etc.)
- Leverages existing Proxmox infrastructure

**Cons**:
- Tied to Proxmox infrastructure
- Requires Proxmox expertise
- More complex networking

**Architecture**:
```
Proxmox API → Proxmox → VM → Guest OS
```

## Future Enhancements

### 1. LXC Runtime Support

Via `proxmox-lxcri` project (priority):

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Postgres
metadata:
  name: my-db
spec:
  runtime: lxc  # NEW: Run in Proxmox LXC
  replicas: 3
```

**Benefits**:
- Lower resource usage than VMs
- Faster startup times
- Better density
- Native Proxmox LXC features

### 2. User Choice Mechanism

Allow users to choose runtime:

```yaml
spec:
  runtime: vm | lxc | pod  # User choice
```

### 3. Advanced Networking

- Direct L2 connectivity to VMs
- VXLAN tunnels
- SR-IOV for high performance

### 4. HA and Failover

- Automatic VM migration on node failure
- Proxmox HA integration
- Storage replication with DRBD

## Troubleshooting

### VM Not Creating

```bash
# Check CAPI provider logs
kubectl -n cozy-cluster-api logs -l control-plane=capmox-controller-manager

# Check ProxmoxMachine status
kubectl describe proxmoxmachine <name>

# Check Proxmox API access
curl -k https://proxmox-host:8006/api2/json/version \
  -H "Authorization: PVEAPIToken=<token>"
```

### Storage Issues

```bash
# Check CSI driver
kubectl -n cozy-proxmox-csi get pods

# Check PVC status
kubectl get pvc
kubectl describe pvc <name>

# Check Proxmox storage
pvesm status
```

### Network Issues

```bash
# Check pod connectivity
kubectl exec -it <pod> -- ping <ip>

# Check Cilium status
kubectl -n cozy-cilium get pods

# Check Kube-OVN
kubectl -n cozy-kubeovn get pods
```

## Best Practices

1. **Use VM templates** - Pre-configure OS images
2. **Separate storage networks** - Dedicated VLAN for storage
3. **Monitor Proxmox resources** - Avoid overprovisioning
4. **Regular backups** - Backup VM templates and configurations
5. **Test failover** - Verify HA works as expected
6. **Document infrastructure** - Keep Proxmox topology documented
7. **Secure API access** - Use tokens with minimal permissions
8. **Monitor API quota** - Proxmox API has rate limits

## References

- [Cluster API Proxmox Provider](https://github.com/ionos-cloud/cluster-api-provider-proxmox)
- [Proxmox CSI Plugin](https://github.com/sergelogvinov/proxmox-csi-plugin)
- [Proxmox CCM](https://github.com/sergelogvinov/proxmox-cloud-controller-manager)
- [Kamaji Documentation](https://github.com/clastix/kamaji)
- [Cluster API Book](https://cluster-api.sigs.k8s.io/)

