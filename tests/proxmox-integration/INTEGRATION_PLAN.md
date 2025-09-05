# Proxmox-Kubernetes Integration Plan

This document outlines the complete integration plan for Proxmox with Kubernetes using CozyStack.

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                              CozyStack Platform                                 │
├─────────────────────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │ Cluster API │  │   Proxmox   │  │    CSI      │  │      Monitoring         │ │
│  │  Provider   │  │ Worker Node │  │   Driver    │  │   (Prometheus +         │ │
│  │  (ionos)    │  │  (kubeadm)  │  │             │  │    Grafana)            │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────────┘ │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │ Networking  │  │  Security   │  │   Storage   │  │      E2E Testing        │ │
│  │ (Cilium +   │  │  Policies   │  │ Management  │  │                         │ │
│  │  Kube-OVN)  │  │    RBAC     │  │             │  │                         │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────────┘
                                        │
                                        ▼
┌─────────────────────────────────────────────────────────────────────────────────┐
│                                Proxmox VE                                       │
├─────────────────────────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────────────────┐ │
│  │   VM Pool   │  │  Network    │  │   Storage   │  │     Management          │ │
│  │ (Kubernetes │  │  Bridges    │  │   Backend   │  │      Interface          │ │
│  │   Tenants)  │  │  (vmbr0+)   │  │ (LVM/ZFS)   │  │    (Web + API)          │ │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────────────────┘ │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## Integration Steps

### Step 1: Proxmox API Connection and Authentication ✅
**Objective**: Establish secure connection to Proxmox VE API

**Components Tested**:
- API connectivity and SSL verification
- Authentication with username/password
- Token-based authentication
- Permission validation
- Response time and reliability

**Test Coverage**:
- `test_proxmox_api.py`: Core API functionality
- Connection timeout handling
- Invalid credentials handling
- Permission checking

### Step 2: Network and Storage Configuration ✅
**Objective**: Configure Proxmox networking and storage for Kubernetes workloads

**Components Tested**:
- Network bridges and VLANs
- Software Defined Networks (SDN)
- Storage pools and content types
- Resource availability
- Integration compatibility

**Test Coverage**:
- `test_proxmox_network_storage.py`: Network and storage validation
- Bridge creation and deletion
- Storage space validation
- Kubernetes requirements verification

### Step 3: VM Creation and Management via Cluster API ✅
**Objective**: Create and manage VMs using Cluster API Proxmox provider

**Components Tested**:
- Cluster API core components
- Proxmox provider installation
- Custom Resource Definitions (CRDs)
- ProxmoxCluster and ProxmoxMachine resources
- VM lifecycle management

**Test Coverage**:
- `test_cluster_api_proxmox.py`: CAPI provider functionality
- Resource creation and management
- VM provisioning status
- Error handling and cleanup

### Step 4: Proxmox Server as Kubernetes Worker ✅
**Objective**: Integrate Proxmox server as worker node via kubeadm

**Components Tested**:
- Helm chart deployment
- Node joining process
- Worker node functionality
- Pod scheduling and resource allocation
- Node labels and taints

**Test Coverage**:
- `test_proxmox_worker.py`: Worker node integration
- Kubernetes prerequisites
- Node readiness validation
- Pod scheduling tests

### Step 5: CSI Storage Integration 🚧
**Objective**: Configure Proxmox CSI for persistent storage

**Components to Test**:
- CSI driver installation
- Storage class creation
- Volume provisioning
- Snapshot functionality
- Backup and restore

**Test Coverage**:
- `test_proxmox_csi.py`: CSI driver functionality
- Dynamic volume provisioning
- Storage performance tests
- Backup and recovery validation

### Step 6: Network Integration and Policies 🚧
**Objective**: Setup network policies and security

**Components to Test**:
- Network policy enforcement
- Cilium and Kube-OVN integration
- Inter-pod communication
- Service mesh capabilities
- Security policy validation

**Test Coverage**:
- `test_network_policies.py`: Network security validation
- Traffic flow tests
- Policy enforcement verification
- Integration with CNI plugins

### Step 7: Monitoring and Logging 🚧
**Objective**: Configure monitoring and logging for Proxmox resources

**Components to Test**:
- Prometheus metrics collection
- Grafana dashboard creation
- Log aggregation
- Alerting configuration
- Performance monitoring

**Test Coverage**:
- `test_monitoring.py`: Monitoring stack validation
- Metrics collection verification
- Dashboard functionality tests
- Alert rule validation

### Step 8: End-to-End Integration Testing 🚧
**Objective**: Full integration validation across all components

**Components to Test**:
- Complete workflow testing
- Performance benchmarking
- Disaster recovery scenarios
- Upgrade procedures
- Security auditing

**Test Coverage**:
- `test_e2e_integration.py`: Complete integration tests
- Performance benchmarks
- Reliability tests
- Security validation

## Test Execution

### Prerequisites
1. **Proxmox VE**: Version 7.0+ with API access
2. **Kubernetes Cluster**: Version 1.26+ with Cluster API installed
3. **Tools**: kubectl, helm, python3, pytest
4. **Network**: Connectivity between Kubernetes and Proxmox
5. **Credentials**: Proxmox API credentials with sufficient permissions

### Setup Test Environment
```bash
# Setup test environment
cd tests/proxmox-integration
./setup-test-env.sh

# Configure credentials
cp config.example.env config.env
# Edit config.env with your values

# Install dependencies
pip3 install -r requirements.txt
```

### Run Tests

#### Run All Tests
```bash
./run-all-tests.sh
```

#### Run Specific Step
```bash
./run-all-tests.sh -s 1  # Run step 1 only
./run-all-tests.sh -s 4  # Run step 4 only
```

#### Run with Custom Configuration
```bash
./run-all-tests.sh -c custom-config.env
```

#### Run Individual Test Files
```bash
# Step 1: API Connection
pytest step1-api-connection/test_proxmox_api.py -v

# Step 2: Network and Storage
pytest step2-network-storage/test_proxmox_network_storage.py -v

# Step 3: VM Management
pytest step3-vm-management/test_cluster_api_proxmox.py -v

# Step 4: Worker Integration
pytest step4-worker-integration/test_proxmox_worker.py -v
```

### Test Reports
Tests generate detailed reports in the `logs/` directory:
- Individual step logs: `stepX-*/test_*.log`
- Summary report: `test_report_TIMESTAMP.md`
- Combined log: `test_run_TIMESTAMP.log`

## Integration Components

### Helm Charts
1. **proxmox-ve**: Basic Proxmox integration with security policies
2. **proxmox-worker**: Proxmox server as Kubernetes worker via kubeadm
3. **capi-providers-proxmox**: Cluster API Proxmox provider
4. **capi-providers-infraprovider**: Unified infrastructure provider

### Key Features
- **VM Management**: Create, update, delete VMs via Cluster API
- **Worker Integration**: Add Proxmox servers as Kubernetes workers
- **Storage**: Persistent volumes via Proxmox CSI
- **Networking**: Bridge-based networking with policy enforcement
- **Monitoring**: Comprehensive monitoring and alerting
- **Security**: RBAC, network policies, and security standards

### Configuration Management
- Environment-based configuration
- Helm values customization
- Secret management
- Credential rotation

## Troubleshooting

### Common Issues
1. **API Connection**: Check firewall, SSL certificates, credentials
2. **Network**: Verify bridge configuration, VLAN setup
3. **Storage**: Confirm storage permissions, space availability
4. **Cluster API**: Check provider installation, CRD versions
5. **Worker Nodes**: Verify kubeadm configuration, join tokens

### Debug Commands
```bash
# Check Proxmox API
curl -k https://proxmox.example.com:8006/api2/json/version

# Check Cluster API
kubectl get clusters,machines,proxmoxclusters,proxmoxmachines -A

# Check worker nodes
kubectl get nodes -o wide
kubectl describe node proxmox-worker

# Check logs
kubectl logs -n proxmox-worker -l app.kubernetes.io/name=proxmox-worker
```

### Support Resources
- [Proxmox VE Documentation](https://pve.proxmox.com/wiki/Main_Page)
- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [Kubernetes Documentation](https://kubernetes.io/docs/)
- [CozyStack Documentation](https://github.com/your-org/cozystack)

## Status Legend
- ✅ **Completed**: Fully implemented and tested
- 🚧 **In Progress**: Under development
- ⏳ **Planned**: Scheduled for future development
- ❌ **Blocked**: Waiting for dependencies or resolution
