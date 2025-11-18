# Proxmox Integration Testing Plan

## 🎯 Testing Overview

This document describes a comprehensive testing plan for Proxmox VE integration with CozyStack platform. Testing is divided into 8 stages, each checking specific aspects of the integration.

## 📋 Testing Structure

### Stage 1: Proxmox API Connection Testing
**Goal**: Verify basic connection and authentication to Proxmox VE API

#### Test Scenarios
1. **API Connectivity Test**
   - Check Proxmox API endpoint availability
   - Test SSL/TLS connection
   - Validate response time (< 2 seconds)
   - Check HTTP status codes

2. **Authentication Test**
   - Test username/password authentication
   - Test token-based authentication
   - Check invalid credentials handling
   - Test session timeout

3. **Permission Validation Test**
   - Check required permissions for Kubernetes
   - Test VM management permissions
   - Check storage access permissions
   - Test network configuration permissions

#### Success Criteria
- ✅ API available with response time < 2s
- ✅ Authentication works for both methods
- ✅ All required permissions granted
- ✅ Error handling works correctly

### Stage 2: Network and Storage Configuration Testing
**Goal**: Validate Proxmox network and storage configuration for Kubernetes

#### Test Scenarios
1. **Network Configuration Test**
   - Check network bridges (vmbr0+)
   - Test VLAN configuration
   - Validate Software Defined Networks (SDN)
   - Check network isolation

2. **Storage Configuration Test**
   - Check storage pools for Kubernetes
   - Test content types (images, templates)
   - Validate storage space availability
   - Check storage permissions

3. **Resource Availability Test**
   - Check CPU resources
   - Test RAM availability
   - Validate disk space
   - Check network bandwidth

#### Success Criteria
- ✅ Network bridges configured correctly
- ✅ Storage pools available and have sufficient space
- ✅ Resources sufficient for Kubernetes workloads
- ✅ Network isolation works

### Stage 3: VM Management via Cluster API Testing
**Goal**: Test VM creation and management through Cluster API Proxmox provider

#### Test Scenarios
1. **Cluster API Components Test**
   - Check CAPI operator installation
   - Test CRD registration
   - Validate InfrastructureProvider
   - Check controller deployment

2. **Proxmox Provider Test**
   - Test ProxmoxCluster resource creation
   - Check ProxmoxMachine resource management
   - Validate VM provisioning process
   - Test VM lifecycle operations

3. **VM Operations Test**
   - Create VM from template
   - Test VM start/stop/restart
   - Check VM configuration updates
   - Test VM deletion

#### Success Criteria
- ✅ CAPI provider installed and working
- ✅ VMs created via Cluster API
- ✅ VM lifecycle operations work
- ✅ Error handling and cleanup work

### Stage 4: Proxmox Worker Integration Testing
**Goal**: Validate Proxmox server as Kubernetes worker node

#### Test Scenarios
1. **Worker Node Setup Test**
   - Test Helm chart deployment
   - Check kubeadm join process
   - Validate node registration
   - Test node readiness

2. **Worker Functionality Test**
   - Check pod scheduling
   - Test resource allocation
   - Validate node labels and taints
   - Check pod execution

3. **Integration Test**
   - Test communication with control plane
   - Check network connectivity
   - Validate storage access
   - Test monitoring integration

#### Success Criteria
- ✅ Proxmox server joined as worker node
- ✅ Pods can be scheduled on worker
- ✅ Resource allocation works correctly
- ✅ Node labels and taints configured

### Stage 5: CSI Storage Integration Testing
**Goal**: Test persistent storage through Proxmox CSI driver

#### Test Scenarios
1. **CSI Driver Test**
   - Check CSI driver installation
   - Test driver health
   - Validate driver capabilities
   - Check driver logs

2. **Storage Class Test**
   - Test storage class creation
   - Check storage class parameters
   - Validate volume provisioning
   - Test volume binding

3. **Volume Operations Test**
   - Test dynamic volume provisioning
   - Check volume mounting
   - Validate volume expansion
   - Test volume snapshots

#### Success Criteria
- ✅ CSI driver installed and healthy
- ✅ Storage classes created and working
- ✅ Dynamic volume provisioning works
- ✅ Volume operations execute successfully

### Stage 6: Network Policies Testing
**Goal**: Validate network policies and CNI integration

#### Test Scenarios
1. **CNI Integration Test**
   - Check CNI plugin installation (Cilium, Kube-OVN)
   - Test pod networking
   - Validate service discovery
   - Check DNS resolution

2. **Network Policy Test**
   - Test network policy creation
   - Check policy enforcement
   - Validate traffic filtering
   - Test policy updates

3. **Security Test**
   - Check pod-to-pod communication
   - Test external access
   - Validate network isolation
   - Check security policies

#### Success Criteria
- ✅ CNI plugins work correctly
- ✅ Network policies are applied
- ✅ Pod networking works
- ✅ Security policies are active

### Stage 7: Monitoring and Logging Testing
**Goal**: Test monitoring and logging for Proxmox resources

#### Test Scenarios
1. **Monitoring Stack Test**
   - Check Prometheus deployment
   - Test Grafana setup
   - Validate metrics collection
   - Check alerting rules

2. **Proxmox Metrics Test**
   - Test Proxmox metrics collection
   - Check node exporter
   - Validate custom metrics
   - Test metrics export

3. **Logging Test**
   - Check log aggregation
   - Test log parsing
   - Validate log retention
   - Check log search

#### Success Criteria
- ✅ Monitoring stack works
- ✅ Proxmox metrics collected
- ✅ Grafana dashboards created
- ✅ Logging works correctly

### Stage 8: End-to-End Integration Testing
**Goal**: Comprehensive testing of entire integration

#### Test Scenarios
1. **Complete Workflow Test**
   - Test complete workload lifecycle
   - Check multi-workload deployment
   - Validate resource management
   - Test scaling operations

2. **Performance Test**
   - Benchmark VM creation time
   - Test storage performance
   - Check network throughput
   - Validate resource utilization

3. **Reliability Test**
   - Test fault tolerance
   - Check recovery procedures
   - Validate backup/restore
   - Test upgrade procedures

#### Success Criteria
- ✅ All components work together
- ✅ Performance meets requirements
- ✅ System is reliable and stable
- ✅ Backup/restore works

## 🧪 Test Environment

### System Requirements
- **Proxmox VE**: 7.0+ with 8GB+ RAM
- **Kubernetes**: 1.26+ with 3+ nodes
- **Network**: Low latency between K8s and Proxmox
- **Storage**: 100GB+ for test VMs

### Test Data
- **VM Templates**: Ubuntu 22.04, CentOS 8
- **Test Workloads**: nginx, redis, postgres
- **Storage Classes**: proxmox-csi, local-storage
- **Network Policies**: deny-all, allow-specific

## 📊 Testing Metrics

### Performance Metrics
- **API Response Time**: < 2 seconds
- **VM Creation Time**: < 5 minutes
- **Volume Provisioning**: < 30 seconds
- **Pod Startup Time**: < 2 minutes

### Reliability Metrics
- **Test Success Rate**: > 95%
- **System Uptime**: > 99%
- **Error Rate**: < 1%
- **Recovery Time**: < 10 minutes

### Resource Metrics
- **CPU Utilization**: < 80%
- **Memory Usage**: < 85%
- **Disk I/O**: < 70%
- **Network Bandwidth**: < 60%

## 🔧 Test Configuration

### Test Parameters
```bash
# Basic parameters
PROXMOX_HOST="192.168.1.100"
PROXMOX_USERNAME="k8s-api@pve"
PROXMOX_PASSWORD="secure-password"
K8S_ENDPOINT="https://k8s-master:6443"

# Test parameters
TEST_NAMESPACE="proxmox-test"
TEST_VM_TEMPLATE="ubuntu-22.04-cloud"
TEST_STORAGE_POOL="proxmox-k8s"
TEST_NETWORK_BRIDGE="vmbr0"

# Performance parameters
PERF_VM_COUNT=10
PERF_VOLUME_SIZE="10Gi"
PERF_TEST_DURATION="300s"
```

### Running Tests
```bash
# All tests
./run-all-tests.sh

# Specific stage
./run-all-tests.sh -s 3

# With detailed logging
./run-all-tests.sh -v

# With resource preservation
KEEP_TEST_RESOURCES=true ./run-all-tests.sh
```

## 📈 Reporting

### Test Reports
- **Individual Test Logs**: `logs/stepX-*/test_*.log`
- **Summary Report**: `logs/test_report_TIMESTAMP.md`
- **Combined Log**: `logs/test_run_TIMESTAMP.log`
- **Performance Report**: `logs/performance_TIMESTAMP.json`

### Report Metrics
- **Test Coverage**: Percentage of test coverage
- **Success Rate**: Percentage of successful tests
- **Performance**: Test execution time
- **Issues**: List of identified problems

## 🚨 Troubleshooting

### Common Issues
1. **API Connection Issues**
   - Check network connectivity
   - Validate SSL certificates
   - Check credentials

2. **CAPI Provider Issues**
   - Check CRD installation
   - Validate controller logs
   - Check permissions

3. **Storage Issues**
   - Check CSI driver
   - Validate storage classes
   - Check volume provisioning

4. **Network Issues**
   - Check CNI plugins
   - Validate network policies
   - Check pod connectivity

### Debug Commands
```bash
# API testing
curl -k -u k8s-api@pve:password https://192.168.1.100:8006/api2/json/version

# CAPI check
kubectl get clusters,machines,proxmoxclusters,proxmoxmachines -A

# Storage check
kubectl get pv,pvc,storageclass,csidriver

# Network check
kubectl get pods -o wide
kubectl get networkpolicy -A
```

## 📚 Documentation

### Test Documents
- **Test Cases**: Detailed test scenarios
- **Test Data**: Test data and configurations
- **Test Results**: Test execution results
- **Troubleshooting Guide**: Problem resolution guide

### Documentation Updates
- After each test cycle
- When new problems are identified
- When configuration changes
- When new tests are added

---

**Last Updated**: 2025-09-10  
**Version**: 1.0.0  
**Author**: CozyStack Team
