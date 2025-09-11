# Proxmox Integration Runbook

## 📋 Overview

This runbook contains step-by-step instructions for installing, configuring, and maintaining Proxmox VE integration with CozyStack platform.

## 🎯 Prerequisites

### System Requirements

#### Proxmox VE Server
- **Version**: 7.0+ (recommended 8.0+)
- **CPU**: 4+ cores (VT-x/AMD-V enabled)
- **RAM**: 8GB+ (recommended 16GB+)
- **Storage**: 100GB+ for VM templates and storage pools
- **Network**: Static IP, access to Kubernetes cluster

#### Kubernetes Cluster (CozyStack)
- **Version**: 1.26+ (recommended 1.28+)
- **Nodes**: 3+ nodes (1 master + 2+ workers)
- **RAM**: 4GB+ per node
- **Storage**: 50GB+ for etcd and logs
- **Network**: Connection to Proxmox server

#### Additional Requirements
- **kubectl**: 1.26+ version
- **helm**: 3.8+ version
- **python3**: 3.8+ version
- **pytest**: for testing
- **curl**: for API testing

### Network Requirements

#### Proxmox VE Ports
- **8006**: Web UI and API (HTTPS)
- **22**: SSH access
- **5900-5999**: VNC console (optional)
- **3128**: Proxmox backup server (optional)

#### Kubernetes Ports
- **6443**: Kubernetes API server
- **2379-2380**: etcd server
- **10250**: kubelet API
- **10251**: kube-scheduler
- **10252**: kube-controller-manager

## 🚀 Installation

### Step 1: Proxmox Server Preparation

#### 1.1 System Check
```bash
# Check Proxmox version
pveversion -v

# Check resources
free -h
df -h
lscpu

# Check network
ip addr show
ip route show
```

#### 1.2 Network Configuration
```bash
# Edit network configuration
nano /etc/network/interfaces

# Example configuration:
# auto vmbr0
# iface vmbr0 inet static
#     address 192.168.1.100/24
#     gateway 192.168.1.1
#     bridge_ports eno1
#     bridge_stp off
#     bridge_fd 0

# Restart network
systemctl restart networking
```

#### 1.3 Storage Pools Setup
```bash
# Check available storage
pvesm status

# Create storage pool for Kubernetes
pvesm add lvm-thin proxmox-k8s --vgname pve --thinpool k8s-thin

# Or use existing storage
pvesm add dir proxmox-k8s --path /var/lib/vz/k8s
```

#### 1.4 API Access Setup
```bash
# Create user for API
pveum user add k8s-api@pve --password 'secure-password'

# Grant permissions
pveum role add Kubernetes --privs "VM.Allocate VM.Clone VM.Config.CDROM VM.Config.CPU VM.Config.Disk VM.Config.Hardware VM.Config.Memory VM.Config.Network VM.Config.Options VM.Monitor VM.PowerMgmt Datastore.AllocateSpace Datastore.Audit Pool.Allocate Sys.Audit Sys.Console Sys.Modify"

# Assign role to user
pveum aclmod / --users k8s-api@pve --roles Kubernetes
```

### Step 2: Kubernetes Cluster Preparation

#### 2.1 Check CozyStack Components
```bash
# Check namespaces
kubectl get namespaces | grep cozy

# Check Cluster API operator
kubectl get pods -n cozy-cluster-api

# Check CAPI providers
kubectl get infrastructureproviders -A
```

#### 2.2 Install Required Components
```bash
# Check available Helm charts
helm list -A | grep -E "(capi|proxmox)"

# If needed, install CAPI operator
helm install capi-operator cozy-capi-operator -n cozy-cluster-api

# Install CAPI providers
helm install capi-providers cozy-capi-providers -n cozy-cluster-api
```

### Step 3: Integration Configuration

#### 3.1 Copy Test Scripts
```bash
# Create working directory
mkdir -p /opt/proxmox-integration
cd /opt/proxmox-integration

# Copy from CozyStack repository
cp -r /path/to/cozystack/tests/proxmox-integration/* .

# Make executable
chmod +x *.sh
```

#### 3.2 Configuration Setup
```bash
# Copy example configuration
cp config.example.env config.env

# Edit configuration
nano config.env
```

**Example config.env:**
```bash
# Proxmox Configuration
PROXMOX_HOST="192.168.1.100"
PROXMOX_PORT="8006"
PROXMOX_USERNAME="k8s-api@pve"
PROXMOX_PASSWORD="secure-password"
PROXMOX_VERIFY_SSL="true"

# Kubernetes Configuration
K8S_ENDPOINT="https://k8s-master:6443"
KUBECONFIG="/root/.kube/config"

# Test Configuration
TEST_NAMESPACE="proxmox-test"
TEST_VM_TEMPLATE="ubuntu-22.04-cloud"
TEST_STORAGE_POOL="proxmox-k8s"
TEST_NETWORK_BRIDGE="vmbr0"

# Storage Configuration
CSI_STORAGE_CLASS="proxmox-csi"
CSI_TEST_SIZE="1Gi"

# Monitoring Configuration
PROMETHEUS_ENDPOINT="http://prometheus:9090"
GRAFANA_ENDPOINT="http://grafana:3000"

# Network Configuration
CNI_PROVIDER="cilium"
NETWORK_POLICY_ENABLED="true"

# E2E Testing
E2E_ENABLE_STORAGE="true"
E2E_ENABLE_NETWORK="true"
E2E_CLEANUP_ON_FAILURE="true"
```

#### 3.3 Install Dependencies
```bash
# Install Python dependencies
pip3 install -r requirements.txt

# Install additional tools
apt-get update
apt-get install -y curl jq openssl
```

### Step 4: Run Integration Tests

#### 4.1 Prepare Test Environment
```bash
# Run setup script
./setup-test-env.sh

# Check preparation
kubectl get namespaces | grep proxmox-test
```

#### 4.2 Sequential Testing
```bash
# Step 1: API connection
./run-all-tests.sh -s 1

# Step 2: Network and storage
./run-all-tests.sh -s 2

# Step 3: VM management
./run-all-tests.sh -s 3

# Step 4: Worker integration
./run-all-tests.sh -s 4

# Step 5: CSI storage
./run-all-tests.sh -s 5

# Step 6: Network policies
./run-all-tests.sh -s 6

# Step 7: Monitoring
./run-all-tests.sh -s 7

# Step 8: E2E testing
./run-all-tests.sh -s 8
```

#### 4.3 Full Testing
```bash
# Run all tests
./run-all-tests.sh

# Run with detailed logging
./run-all-tests.sh -v

# Run with resource preservation for debugging
KEEP_TEST_RESOURCES=true ./run-all-tests.sh
```

## 🔧 Component Configuration

### Cluster API Proxmox Provider

#### Installation
```bash
# Deploy CAPI Proxmox provider
helm install capi-providers-proxmox cozy-capi-providers-proxmox \
  -n cozy-cluster-api \
  --set proxmox.enabled=true \
  --set kubevirt.enabled=false
```

#### Verify Installation
```bash
# Check CRDs
kubectl get crd | grep proxmox

# Check InfrastructureProvider
kubectl get infrastructureproviders

# Check pods
kubectl get pods -n cozy-cluster-api | grep proxmox
```

### Proxmox Worker Node

#### Installation
```bash
# Deploy Proxmox worker chart
helm install proxmox-worker proxmox-worker \
  -n cozy-proxmox \
  --set proxmox.host="192.168.1.100" \
  --set proxmox.username="k8s-api@pve" \
  --set proxmox.password="secure-password"
```

#### Verify Worker Node
```bash
# Check node status
kubectl get nodes -o wide

# Check labels and taints
kubectl describe node proxmox-worker

# Check pod scheduling
kubectl get pods -o wide | grep proxmox-worker
```

### CSI Storage Driver

#### Installation
```bash
# Deploy Proxmox CSI operator
helm install proxmox-csi-operator cozy-proxmox-csi-operator \
  -n cozy-proxmox \
  --set proxmox.host="192.168.1.100" \
  --set proxmox.username="k8s-api@pve" \
  --set proxmox.password="secure-password"
```

#### Configure Storage Class
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: proxmox-csi
provisioner: proxmox.csi.io
parameters:
  storage: proxmox-k8s
  content: images
reclaimPolicy: Delete
allowVolumeExpansion: true
```

#### Verify CSI
```bash
# Check CSI driver
kubectl get csidriver

# Check storage class
kubectl get storageclass

# Test volume provisioning
kubectl apply -f - <<EOF
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
  storageClassName: proxmox-csi
EOF
```

## 🔍 Monitoring and Diagnostics

### Check Component Status

#### Proxmox Server
```bash
# Service status
systemctl status pve-cluster
systemctl status pveproxy
systemctl status pvedaemon

# Logs
journalctl -u pve-cluster -f
journalctl -u pveproxy -f
```

#### Kubernetes Cluster
```bash
# Pod status
kubectl get pods -A | grep -E "(proxmox|capi)"

# CAPI provider logs
kubectl logs -n cozy-cluster-api -l app.kubernetes.io/name=capi-providers-proxmox

# CSI driver logs
kubectl logs -n cozy-proxmox -l app.kubernetes.io/name=proxmox-csi-operator
```

### Metrics and Monitoring

#### Prometheus Metrics
```bash
# Check Proxmox metrics
curl -k https://192.168.1.100:8006/api2/json/version

# Check Kubernetes metrics
kubectl get --raw /metrics

# Check CAPI metrics
kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes
```

#### Grafana Dashboard
```bash
# Access Grafana
kubectl port-forward -n cozy-monitoring svc/grafana 3000:80

# Open in browser
# http://localhost:3000
```

## 🚨 Troubleshooting

### Common Issues

#### 1. API Connection Not Working
```bash
# Check network connectivity
ping 192.168.1.100
telnet 192.168.1.100 8006

# Check SSL certificates
openssl s_client -connect 192.168.1.100:8006 -servername 192.168.1.100

# Test API
curl -k -u k8s-api@pve:secure-password https://192.168.1.100:8006/api2/json/version
```

#### 2. CAPI Provider Not Installing
```bash
# Check CRDs
kubectl get crd | grep cluster

# Check pods
kubectl get pods -n cozy-cluster-api

# Logs
kubectl logs -n cozy-cluster-api -l app.kubernetes.io/name=capi-operator
```

#### 3. Worker Node Not Joining
```bash
# Check kubeadm configuration
kubectl get nodes
kubectl describe node proxmox-worker

# Check join token
kubeadm token list

# Kubelet logs
journalctl -u kubelet -f
```

#### 4. CSI Storage Not Working
```bash
# Check CSI driver
kubectl get csidriver
kubectl get pods -n cozy-proxmox

# Check storage class
kubectl get storageclass
kubectl describe storageclass proxmox-csi

# CSI driver logs
kubectl logs -n cozy-proxmox -l app.kubernetes.io/name=proxmox-csi-operator
```

### Diagnostic Commands

#### Check Proxmox
```bash
# System status
pveversion -v
pveceph status
pvesm status

# VM status
qm list
qm status <vmid>

# Network configuration
cat /etc/network/interfaces
ip addr show
```

#### Check Kubernetes
```bash
# Cluster status
kubectl cluster-info
kubectl get nodes -o wide
kubectl get pods -A

# CAPI status
kubectl get clusters,machines,proxmoxclusters,proxmoxmachines -A

# Storage status
kubectl get pv,pvc,storageclass
kubectl get csidriver
```

## 🔄 Maintenance

### Regular Tasks

#### Daily Checks
```bash
# Daily health check script
#!/bin/bash
echo "=== Proxmox Integration Health Check ==="

# Check Proxmox API
curl -k -s -u k8s-api@pve:secure-password https://192.168.1.100:8006/api2/json/version > /dev/null
if [ $? -eq 0 ]; then
    echo "✅ Proxmox API: OK"
else
    echo "❌ Proxmox API: FAILED"
fi

# Check Kubernetes API
kubectl cluster-info > /dev/null 2>&1
if [ $? -eq 0 ]; then
    echo "✅ Kubernetes API: OK"
else
    echo "❌ Kubernetes API: FAILED"
fi

# Check CAPI provider
kubectl get infrastructureproviders > /dev/null 2>&1
if [ $? -eq 0 ]; then
    echo "✅ CAPI Provider: OK"
else
    echo "❌ CAPI Provider: FAILED"
fi

# Check worker nodes
kubectl get nodes | grep proxmox-worker > /dev/null
if [ $? -eq 0 ]; then
    echo "✅ Proxmox Worker: OK"
else
    echo "❌ Proxmox Worker: FAILED"
fi

# Check CSI driver
kubectl get csidriver | grep proxmox > /dev/null
if [ $? -eq 0 ]; then
    echo "✅ CSI Driver: OK"
else
    echo "❌ CSI Driver: FAILED"
fi

echo "=== Health Check Complete ==="
```

#### Weekly Tasks
- Clean old logs
- Check disk space
- Update configuration backups
- Analyze metrics and performance

#### Monthly Tasks
- Update components
- Security audit
- Performance tuning
- Documentation updates

### Backup and Recovery

#### Configuration Backup
```bash
# Backup Proxmox configuration
tar -czf proxmox-config-$(date +%Y%m%d).tar.gz /etc/pve/

# Backup Kubernetes configuration
kubectl get all -A -o yaml > k8s-config-$(date +%Y%m%d).yaml

# Backup Helm releases
helm list -A -o yaml > helm-releases-$(date +%Y%m%d).yaml
```

#### Recovery
```bash
# Restore Proxmox configuration
tar -xzf proxmox-config-YYYYMMDD.tar.gz -C /

# Restore Kubernetes resources
kubectl apply -f k8s-config-YYYYMMDD.yaml

# Restore Helm releases
helm install -f helm-releases-YYYYMMDD.yaml
```

## 📚 Additional Resources

### Documentation
- [Proxmox VE Documentation](https://pve.proxmox.com/wiki/Main_Page)
- [Kubernetes Documentation](https://kubernetes.io/docs/)
- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [CozyStack Documentation](https://github.com/cozystack/cozystack)

### Useful Links
- [Proxmox API Reference](https://pve.proxmox.com/wiki/Proxmox_VE_API)
- [Kubernetes API Reference](https://kubernetes.io/docs/reference/)
- [Cluster API Providers](https://cluster-api.sigs.k8s.io/reference/providers.html)

### Support
- **GitHub Issues**: [CozyStack Repository](https://github.com/cozystack/cozystack/issues)
- **Slack**: #proxmox-integration
- **Email**: support@cozystack.io

---

**Last Updated**: 2025-09-10  
**Version**: 1.0.0  
**Author**: CozyStack Team
