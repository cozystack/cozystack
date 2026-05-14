# Extended Proxmox Integration Plan

**Date**: 2025-10-24  
**Phase**: Advanced Integration - Tenants and LXC Runtime  
**Status**: Planning Phase

## 🎯 Extended Integration Goals

### 1. Tenant Kubernetes Clusters via Cluster API
Enable provisioning of tenant Kubernetes clusters on Proxmox VMs through Cluster API

### 2. LXC as a Pod Pattern
Implement OCI runtime for running LXC containers as Kubernetes pods for database services

### 3. Database-as-a-Service with User Choice
Allow users to choose between VM or LXC deployment for database operators

## 📊 Current Operator Inventory

### Database Operators (Already Installed)

#### 1. PostgreSQL Operator ✅
- **Name**: postgres-operator-cloudnative-pg
- **Namespace**: cozy-postgres-operator
- **Status**: 1/1 Running
- **CRD**: Cluster (postgresql.cnpg.io)
- **Potential**: Can run in LXC

#### 2. MariaDB Operator ✅
- **Name**: mariadb-operator
- **Namespace**: cozy-mariadb-operator
- **Status**: 1/1 Running (+ cert-controller + webhook)
- **CRD**: MariaDB, Database
- **Potential**: Can run in LXC

#### 3. Redis Operator ✅
- **Name**: redis-operator
- **Namespace**: cozy-redis-operator
- **Status**: 0/1 (ImagePullBackOff)
- **CRD**: Redis instances
- **Potential**: Can run in LXC

#### 4. RabbitMQ Operator ✅
- **Name**: rabbitmq-cluster-operator
- **Namespace**: cozy-rabbitmq-operator
- **Status**: 1/1 Running
- **CRD**: RabbitmqCluster
- **Potential**: Can run in LXC

#### 5. ClickHouse Operator ✅
- **Name**: clickhouse-operator-altinity
- **Namespace**: cozy-clickhouse-operator
- **Status**: 1/1 Running
- **CRD**: ClickHouseInstallation
- **Potential**: Can run in LXC

#### 6. Kafka Operator ✅
- **Name**: strimzi-cluster-operator
- **Namespace**: cozy-kafka-operator
- **Status**: 1/1 Running
- **CRD**: Kafka, KafkaConnect
- **Potential**: Can run in LXC

### Infrastructure Operators

#### 7. ETCD Operator ✅
- **Namespace**: cozy-etcd-operator
- **Status**: 0/1 (ImagePullBackOff)
- **Potential**: Can run in LXC

#### 8. Grafana Operator ✅
- **Namespace**: cozy-grafana-operator
- **Status**: 1/1 Running
- **Potential**: VM preferred

#### 9. Victoria Metrics Operator ✅
- **Namespace**: cozy-victoria-metrics-operator
- **Status**: 0/1 (ImagePullBackOff)
- **Potential**: VM preferred

### Virtualization Operators

#### 10. KubeVirt Operator ✅
- **Namespace**: cozy-kubevirt
- **Status**: 2/2 Running (virt-operator)
- **Purpose**: VM management
- **Note**: Will complement Proxmox, not replace

## 🏗️ Extended Architecture

### Current Architecture
```
┌──────────────────────────────────────────────────────┐
│         CozyStack Management Cluster                 │
│         (Talos VMs + Proxmox Worker)                 │
│                                                      │
│  ┌────────────────────────────────────────────────┐ │
│  │        Cluster API + Proxmox Provider          │ │
│  │        (VM Provisioning)                       │ │
│  └────────────────────────────────────────────────┘ │
│                                                      │
│  ┌────────────────────────────────────────────────┐ │
│  │    Database Operators (PostgreSQL, MariaDB,    │ │
│  │    Redis, RabbitMQ, ClickHouse, Kafka)         │ │
│  │    Currently running in regular pods           │ │
│  └────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────┘
```

### Target Extended Architecture
```
┌─────────────────────────────────────────────────────────────────┐
│              CozyStack Management Cluster                       │
│              (Talos VMs + Proxmox Worker)                       │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Cluster API + Proxmox Provider + Kamaji                 │  │
│  │  (Tenant Cluster Provisioning)                           │  │
│  └──────────────────────────────────────────────────────────┘  │
│           │                                                     │
│           ├─► Tenant Cluster 1 (VMs on Proxmox)               │
│           ├─► Tenant Cluster 2 (VMs on Proxmox)               │
│           └─► Tenant Cluster N (VMs on Proxmox)               │
│                                                                 │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │  Database Operators with Dual Runtime Support            │  │
│  │                                                           │  │
│  │  User Choice:                                             │  │
│  │  ┌────────────────┐         ┌────────────────┐          │  │
│  │  │  LXC Runtime   │   OR    │   VM Runtime   │          │  │
│  │  │  (Lightweight) │         │   (Isolated)   │          │  │
│  │  └────────────────┘         └────────────────┘          │  │
│  │                                                           │  │
│  │  Databases in LXC:          Databases in VMs:            │  │
│  │  - PostgreSQL               - ClickHouse (heavy)         │  │
│  │  - MariaDB                  - Kafka (production)         │  │
│  │  - Redis                    - ETCD (critical)            │  │
│  │  - RabbitMQ                                              │  │
│  └──────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ▼
                    ┌──────────────────┐
                    │   Proxmox VE     │
                    │   Host: mgr      │
                    │                  │
                    │  ├─ VMs (tenant) │
                    │  └─ LXC (DBs)    │
                    └──────────────────┘
```

## 🔧 Component 1: Tenant Cluster Provisioning

### Requirements
- Provision tenant Kubernetes clusters on Proxmox VMs
- Use Kamaji for control planes
- Use Cluster API for VM lifecycle
- Integrate with Proxmox provider

### Implementation Plan

#### Step 1: Kamaji Integration
```yaml
apiVersion: kamaji.clastix.io/v1alpha1
kind: TenantControlPlane
metadata:
  name: tenant-cluster-1
spec:
  controlPlane:
    deployment:
      replicas: 2
    service:
      serviceType: LoadBalancer
  kubernetes:
    version: "1.32.0"
  addons:
    coreDNS: {}
    kubeProxy: {}
```

#### Step 2: Proxmox VM Provisioning
```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: tenant-cluster-1
spec:
  controlPlaneRef:
    apiVersion: kamaji.clastix.io/v1alpha1
    kind: TenantControlPlane
    name: tenant-cluster-1
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
    kind: ProxmoxCluster
    name: tenant-cluster-1-infra
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxCluster
metadata:
  name: tenant-cluster-1-infra
spec:
  controlPlaneEndpoint:
    host: <kamaji-service-ip>
    port: 6443
  allowedNodes:
    - mgr
  ipv4Config:
    addresses:
      - 10.0.0.200-10.0.0.220
    gateway: 10.0.0.1
    prefix: 24
---
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: tenant-cluster-1-workers
spec:
  clusterName: tenant-cluster-1
  replicas: 3
  selector:
    matchLabels: {}
  template:
    spec:
      clusterName: tenant-cluster-1
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: tenant-cluster-1-worker-bootstrap
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
        kind: ProxmoxMachineTemplate
        name: tenant-cluster-1-worker
---
apiVersion: infrastructure.cluster.x-k8s.io/v1alpha1
kind: ProxmoxMachineTemplate
metadata:
  name: tenant-cluster-1-worker
spec:
  template:
    spec:
      sourceNode: mgr
      templateID: 201  # ubuntu22-k8s-template
      cores: 2
      memory: 4096
      disk:
        size: 20G
      network:
        default:
          bridge: vmbr0
          model: virtio
```

### Testing Plan for Tenant Clusters

#### Test 1: Kamaji Control Plane
- [ ] Create TenantControlPlane resource
- [ ] Verify control plane pods running
- [ ] Get kubeconfig for tenant
- [ ] Test kubectl access

#### Test 2: ProxmoxMachine Creation
- [ ] Create ProxmoxMachine for worker
- [ ] Verify VM created in Proxmox
- [ ] Check VM configuration
- [ ] Verify VM started

#### Test 3: Worker Node Join
- [ ] Verify worker joined tenant cluster
- [ ] Check node Ready status
- [ ] Test pod scheduling on tenant cluster

#### Test 4: Tenant Cluster Validation
- [ ] Deploy sample workload
- [ ] Test service exposure
- [ ] Verify network connectivity
- [ ] Test persistent storage

## 🔧 Component 2: LXC as a Pod Runtime

### Research: LXC OCI Runtime Options

#### Option A: lxc-ri (LXC Runtime Interface)
**Project**: Custom OCI runtime wrapper for LXC  
**Status**: Need to develop or find existing

**Pros**:
- True LXC containers
- Lightweight
- Fast startup

**Cons**:
- Need custom OCI runtime
- Kubernetes integration complex
- Limited existing solutions

#### Option B: Incus (LXD successor)
**Project**: https://github.com/lxc/incus  
**Status**: Active development

**Pros**:
- Modern LXC/LXD alternative
- Good API
- Active community

**Cons**:
- Still VM-like (not pod-like)
- Need CRI adapter

#### Option C: Kata Containers with Firecracker
**Project**: https://github.com/kata-containers/kata-containers  
**Status**: Mature

**Pros**:
- Lightweight VM isolation
- Kubernetes native
- Production ready

**Cons**:
- VMs not containers
- More overhead than LXC

#### Option D: RuntimeClass + Custom Runtime
**Recommended Approach**:
- Use Kubernetes RuntimeClass
- Implement custom containerd runtime
- Integrate with Proxmox LXC

### Implementation Plan for LXC Runtime

#### Phase 1: Research and POC (1 week)
1. [ ] Research existing LXC OCI runtimes
2. [ ] Evaluate proxmox-lxcri project potential
3. [ ] Create POC with RuntimeClass
4. [ ] Test basic LXC pod creation

#### Phase 2: Runtime Development (2-3 weeks)
1. [ ] Develop/adapt LXC OCI runtime
2. [ ] Integrate with containerd
3. [ ] Create RuntimeClass definitions
4. [ ] Test with simple workloads

#### Phase 3: Operator Integration (2 weeks)
1. [ ] Modify database operators for RuntimeClass
2. [ ] Add user choice mechanism
3. [ ] Test each database in LXC
4. [ ] Performance comparison

#### Phase 4: Production Validation (1 week)
1. [ ] Full testing suite
2. [ ] Performance benchmarks
3. [ ] Security audit
4. [ ] Documentation

### RuntimeClass Configuration Example

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: proxmox-lxc
handler: proxmox-lxc
scheduling:
  nodeSelector:
    runtime.proxmox.io/lxc: "true"
  tolerations:
    - key: runtime.proxmox.io/lxc
      operator: Exists
---
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgresql-lxc
spec:
  template:
    spec:
      runtimeClassName: proxmox-lxc  # Use LXC runtime
      containers:
      - name: postgresql
        image: postgres:15
        # ... rest of config
```

### User Choice Mechanism

```yaml
apiVersion: cozystack.io/v1alpha1
kind: PostgreSQL
metadata:
  name: my-database
spec:
  runtime: lxc  # or: vm, pod (default)
  replicas: 3
  resources:
    memory: 4Gi
    cpu: 2
  storage:
    size: 100Gi
    storageClass: proxmox-data
```

## 📋 Detailed Implementation Tasks

### Task Group A: Tenant Cluster Provisioning

#### A.1: Kamaji Configuration
- [ ] Verify Kamaji operator running
- [ ] Create TenantControlPlane template
- [ ] Configure datastore (etcd)
- [ ] Test control plane creation

#### A.2: CAPI Integration for Tenants
- [ ] Create ProxmoxMachineTemplate for workers
- [ ] Configure bootstrap (kubeadm)
- [ ] Setup networking for tenant VMs
- [ ] Test VM provisioning

#### A.3: Tenant Cluster Lifecycle
- [ ] Create tenant cluster creation workflow
- [ ] Implement cluster deletion
- [ ] Handle scaling (add/remove workers)
- [ ] Backup and restore procedures

#### A.4: Testing
- [ ] End-to-end tenant cluster creation
- [ ] Multi-tenant isolation validation
- [ ] Network connectivity between tenants
- [ ] Resource quota enforcement

**Estimated Time**: 2-3 weeks  
**Priority**: High  
**Dependencies**: Proxmox CAPI provider stable

### Task Group B: LXC Runtime Integration

#### B.1: proxmox-lxcri Development
**Reference**: Your current proxmox-lxcri project (priority)

- [ ] Complete proxmox-lxcri OCI runtime
- [ ] Integrate with containerd
- [ ] Create RuntimeClass definitions
- [ ] Test basic LXC pod

**Estimated Time**: Per proxmox-lxcri project timeline  
**Priority**: **CURRENT PRIORITY** (blocking this work)

#### B.2: Proxmox LXC API Integration
- [ ] Proxmox LXC template creation
- [ ] LXC configuration for Kubernetes
- [ ] Network integration (Kube-OVN + LXC)
- [ ] Storage integration (volumes in LXC)

#### B.3: RuntimeClass Deployment
- [ ] Create RuntimeClass resources
- [ ] Configure node labels/taints
- [ ] Setup admission controllers
- [ ] Validate pod scheduling

#### B.4: Operator Adaptation
- [ ] Modify PostgreSQL operator
- [ ] Modify MariaDB operator
- [ ] Add runtime selector to CRDs
- [ ] Test each operator with LXC

**Estimated Time**: 3-4 weeks (after lxcri complete)  
**Priority**: High  
**Dependencies**: proxmox-lxcri completion

### Task Group C: User Choice Mechanism

#### C.1: CRD Extensions
- [ ] Add runtime field to database CRDs
- [ ] Implement runtime selector logic
- [ ] Add validation webhooks
- [ ] Default runtime policies

#### C.2: Admission Controller
- [ ] Develop admission webhook
- [ ] Runtime selection logic
- [ ] Resource validation
- [ ] Quota enforcement

#### C.3: Dashboard Integration
- [ ] UI for runtime selection
- [ ] Runtime status display
- [ ] Performance metrics
- [ ] Cost comparison

**Estimated Time**: 2 weeks  
**Priority**: Medium  
**Dependencies**: LXC runtime working

## 🧪 Extended Testing Plan

### Test Suite 1: Tenant Cluster Provisioning

#### Test 1.1: Single Tenant Cluster
```python
def test_create_tenant_cluster():
    """Test creating a single tenant cluster"""
    # Create TenantControlPlane
    # Create ProxmoxCluster for infra
    # Create MachineDeployment for workers
    # Verify VMs created in Proxmox
    # Verify cluster accessible
    # Test workload deployment
```

#### Test 1.2: Multi-Tenant Isolation
```python
def test_multi_tenant_isolation():
    """Test isolation between tenant clusters"""
    # Create 2 tenant clusters
    # Deploy services in each
    # Verify network isolation
    # Test resource quotas
    # Verify no cross-tenant access
```

#### Test 1.3: Tenant Cluster Scaling
```python
def test_tenant_cluster_scaling():
    """Test scaling tenant cluster workers"""
    # Create tenant cluster with 1 worker
    # Scale to 3 workers
    # Verify new VMs created
    # Verify workers joined
    # Scale down to 2
    # Verify VM deletion
```

#### Test 1.4: Tenant Cluster Deletion
```python
def test_tenant_cluster_deletion():
    """Test complete tenant cluster cleanup"""
    # Create tenant cluster
    # Deploy workloads
    # Delete cluster
    # Verify VMs deleted in Proxmox
    # Verify no resources remain
```

### Test Suite 2: LXC Runtime

#### Test 2.1: LXC Pod Creation
```python
def test_lxc_pod_creation():
    """Test creating pod with LXC runtime"""
    # Create pod with runtimeClassName: proxmox-lxc
    # Verify LXC container created in Proxmox
    # Verify pod Running
    # Test container exec
    # Test logging
```

#### Test 2.2: Database in LXC
```python
def test_postgresql_in_lxc():
    """Test PostgreSQL running in LXC"""
    # Create PostgreSQL with runtime: lxc
    # Verify LXC container created
    # Test database connectivity
    # Test persistence
    # Test backups
```

#### Test 2.3: LXC vs VM Performance
```python
def test_lxc_vs_vm_performance():
    """Compare LXC and VM performance"""
    # Deploy same database in LXC
    # Deploy same database in VM
    # Run performance benchmarks
    # Compare startup time
    # Compare resource usage
    # Compare I/O performance
```

#### Test 2.4: LXC Pod Lifecycle
```python
def test_lxc_pod_lifecycle():
    """Test complete LXC pod lifecycle"""
    # Create LXC pod
    # Test restart
    # Test stop/start
    # Test migration (if supported)
    # Delete pod
    # Verify cleanup
```

### Test Suite 3: User Choice Mechanism

#### Test 3.1: Runtime Selection
```python
def test_runtime_selection():
    """Test user can choose runtime"""
    # Create database with runtime: lxc
    # Verify LXC used
    # Create database with runtime: vm
    # Verify VM used
    # Create database with runtime: pod (default)
    # Verify regular pod used
```

#### Test 3.2: Runtime Constraints
```python
def test_runtime_constraints():
    """Test runtime constraints and validation"""
    # Try LXC on non-Proxmox node (should fail)
    # Try VM without enough resources (should fail)
    # Verify validation webhooks
    # Test quota enforcement
```

## 🔍 Extended Integrity Checks

### New Check Categories

#### Category: Tenant Clusters (10 checks)
1. ✅ Kamaji operator running
2. ✅ TenantControlPlane CRD installed
3. ✅ Tenant clusters count
4. ✅ Tenant control planes healthy
5. ✅ Tenant worker VMs provisioned
6. ✅ Tenant cluster connectivity
7. ✅ Tenant isolation validation
8. ✅ Tenant resource quotas
9. ✅ Tenant networking
10. ✅ Tenant storage

#### Category: LXC Runtime (8 checks)
1. ✅ proxmox-lxcri runtime available
2. ✅ RuntimeClass resources created
3. ✅ LXC templates in Proxmox
4. ✅ LXC pods running
5. ✅ LXC container health
6. ✅ LXC networking functional
7. ✅ LXC storage mounted
8. ✅ LXC vs Pod performance

#### Category: Database Services (12 checks)
1. ✅ PostgreSQL operator functional
2. ✅ MariaDB operator functional
3. ✅ Redis operator functional
4. ✅ RabbitMQ operator functional
5. ✅ ClickHouse operator functional
6. ✅ Kafka operator functional
7. ✅ Databases deployable in LXC
8. ✅ Databases deployable in VM
9. ✅ Runtime selection working
10. ✅ Database connectivity
11. ✅ Database persistence
12. ✅ Database backups

**Total New Checks**: 30+  
**Grand Total**: 80+ comprehensive checks

## 📝 Extended Documentation Needs

### New Documents to Create

1. **TENANT_CLUSTER_GUIDE.md**
   - How to provision tenant clusters
   - Kamaji + CAPI integration
   - Best practices
   - Troubleshooting

2. **LXC_RUNTIME_GUIDE.md**
   - proxmox-lxcri integration
   - RuntimeClass configuration
   - LXC template preparation
   - Performance tuning

3. **DATABASE_DEPLOYMENT_GUIDE.md**
   - Runtime selection guide
   - LXC vs VM decision matrix
   - Operator configuration
   - Migration procedures

4. **EXTENDED_TESTING_PLAN.md**
   - Tenant cluster tests
   - LXC runtime tests
   - Performance benchmarks
   - Security validation

5. **EXTENDED_INTEGRITY_CHECKS.md**
   - New check categories
   - Tenant validation
   - LXC health checks
   - Database service checks

## 🎯 Implementation Timeline

### Phase 0: Current State (Complete)
**Status**: ✅ 90% Integration Complete  
**Duration**: Completed

- [x] Basic Proxmox integration
- [x] CAPI provider operational
- [x] CSI/CCM installed
- [x] Worker node integrated

### Phase 1: proxmox-lxcri Completion (PRIORITY)
**Status**: 🚧 In Progress (your current focus)  
**Duration**: Per project timeline  
**Blocking**: All LXC-related work

**Tasks**:
- Complete OCI runtime implementation
- Containerd integration
- Basic testing
- Documentation

**Deliverable**: Working proxmox-lxcri runtime

### Phase 2: Tenant Cluster Provisioning
**Status**: ⏳ Planned (after Phase 1)  
**Duration**: 2-3 weeks  
**Dependencies**: Stable Proxmox CAPI provider

**Tasks**:
- Kamaji configuration
- ProxmoxMachine templates
- Tenant cluster workflows
- Multi-tenant testing

**Deliverable**: Tenant cluster provisioning working

### Phase 3: LXC Runtime Integration
**Status**: ⏳ Planned (after Phase 1)  
**Duration**: 3-4 weeks  
**Dependencies**: proxmox-lxcri complete

**Tasks**:
- RuntimeClass setup
- LXC template creation
- Operator adaptation
- Performance testing

**Deliverable**: Databases running in LXC

### Phase 4: User Choice Mechanism
**Status**: ⏳ Planned (after Phase 3)  
**Duration**: 2 weeks  
**Dependencies**: LXC runtime working

**Tasks**:
- CRD extensions
- Admission webhooks
- Dashboard integration
- Documentation

**Deliverable**: User can choose LXC/VM/Pod

### Phase 5: Production Rollout
**Status**: ⏳ Planned (after Phase 4)  
**Duration**: 1 week

**Tasks**:
- Complete testing
- Performance tuning
- Security audit
- Team training

**Deliverable**: Production-ready extended integration

## 📊 Estimated Timeline

### Optimistic (3 months)
- Phase 1 (lxcri): 4 weeks
- Phase 2 (tenants): 2 weeks
- Phase 3 (LXC integration): 3 weeks
- Phase 4 (user choice): 2 weeks
- Phase 5 (production): 1 week
**Total**: 12 weeks

### Realistic (4-5 months)
- Phase 1: 6 weeks (with testing)
- Phase 2: 3 weeks (with debugging)
- Phase 3: 4 weeks (with operator work)
- Phase 4: 2 weeks
- Phase 5: 1 week
**Total**: 16 weeks

### Conservative (6 months)
- Buffer for unexpected issues
- Thorough testing
- Community feedback
- Production hardening
**Total**: 24 weeks

## 🎯 Success Criteria

### Tenant Cluster Provisioning
- [ ] Can create tenant cluster via kubectl
- [ ] VMs provisioned automatically in Proxmox
- [ ] Workers join tenant cluster
- [ ] Full network isolation
- [ ] Storage working in tenant
- [ ] Can delete cluster cleanly

### LXC Runtime
- [ ] proxmox-lxcri runtime functional
- [ ] Can create LXC pods via RuntimeClass
- [ ] Databases run in LXC containers
- [ ] Performance better than VMs
- [ ] Security equivalent to containers

### User Choice
- [ ] User can select runtime in CRD
- [ ] Admission controller enforces policies
- [ ] Dashboard shows runtime options
- [ ] Migration between runtimes possible
- [ ] Documentation clear

## 📚 Reference Projects

### For Tenant Clusters
- **Kamaji**: https://kamaji.clastix.io/
- **Cluster API**: https://cluster-api.sigs.k8s.io/
- **cluster-api-provider-proxmox**: https://github.com/ionos-cloud/cluster-api-provider-proxmox

### For LXC Runtime
- **proxmox-lxcri**: Your current project (PRIORITY)
- **LXC**: https://linuxcontainers.org/lxc/
- **Incus**: https://github.com/lxc/incus
- **OCI Runtime Spec**: https://github.com/opencontainers/runtime-spec

### For Database Operators
- **CloudNativePG**: https://cloudnative-pg.io/
- **MariaDB Operator**: https://github.com/mariadb-operator/mariadb-operator
- **Redis Operator**: https://github.com/spotahome/redis-operator

## 🚨 Critical Dependencies

### 1. proxmox-lxcri Project (BLOCKING)
**Status**: Your current priority  
**Impact**: Blocks all LXC-related work  
**Timeline**: Per project schedule

**What it provides**:
- OCI runtime for LXC
- Containerd integration
- Proxmox LXC management
- Pod-like LXC containers

**Recommendation**: **Complete proxmox-lxcri first**, then return to extended integration

### 2. Stable VM Provisioning
**Status**: 70% complete  
**Impact**: Needed for reliable tenant clusters  
**Timeline**: 1-2 weeks debugging

**What needs work**:
- VM creation automation
- CAPI provider stability
- Error handling
- Cleanup procedures

## 🎯 Recommended Approach

### Option A: Sequential (Recommended)
1. **Now**: Focus on proxmox-lxcri (your priority)
2. **Next**: Tenant cluster provisioning (2-3 weeks)
3. **Then**: LXC runtime integration (3-4 weeks)
4. **Finally**: User choice mechanism (2 weeks)

**Total**: 4-5 months  
**Benefits**: Solid foundation, less risk

### Option B: Parallel
1. **Team A**: proxmox-lxcri development
2. **Team B**: Tenant cluster provisioning
3. **Merge**: When lxcri complete

**Total**: 2-3 months  
**Benefits**: Faster, but needs more resources

### Option C: MVP First
1. **MVP**: Tenant clusters only (no LXC)
2. **V2**: Add LXC runtime
3. **V3**: Add user choice

**Total**: Incremental delivery  
**Benefits**: Early value, iterate

## 📝 Immediate Next Steps

### This Week (Priority: proxmox-lxcri)
1. Continue proxmox-lxcri development
2. Document current Proxmox integration
3. Plan tenant cluster architecture
4. Research LXC best practices

### After proxmox-lxcri Complete
1. Test LXC pod creation
2. Start tenant cluster implementation
3. Begin operator adaptation
4. Expand testing framework

### Continuous
1. Monitor current integration (90%)
2. Fix registry access when possible
3. Maintain documentation
4. Community engagement

---

**Extended Integration Complexity**: High  
**Estimated Timeline**: 4-5 months  
**Current Blocker**: proxmox-lxcri completion  
**Recommendation**: Focus on lxcri, plan in parallel

**Next Document**: Create detailed tenant cluster guide after lxcri complete
