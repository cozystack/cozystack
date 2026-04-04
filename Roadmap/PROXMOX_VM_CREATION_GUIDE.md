# Proxmox VM Creation Guide

## Overview

This guide explains how to create Virtual Machines in Proxmox through CozyStack's Kubernetes API using Cluster API Provider for Proxmox.

**Important**: VMs are created **directly in Proxmox hypervisor**, not through KubeVirt. This provides better performance and leverages Proxmox's native VM management capabilities.

## Prerequisites

- CozyStack with `paas-proxmox` bundle installed
- Proxmox VE server (7.0+) with API access
- Cluster API Proxmox provider running
- ProxmoxCluster configured
- VM templates created in Proxmox

## Architecture

```
User → kubectl apply → CAPI → capmox provider → Proxmox API → Proxmox VM
```

**NOT**:
```
User → kubectl apply → KubeVirt → Pod → QEMU → VM  ❌
```

## VM Creation Methods

### Method 1: Tenant Kubernetes Cluster (Recommended)

Create a full Kubernetes cluster with VMs as worker nodes:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: tenant-cluster
  namespace: tenant-demo
spec:
  # Number of control plane nodes (runs as pods via Kamaji)
  replicas: 3
  
  # Kubernetes version
  kubernetesVersion: v1.28.0
  
  # Node groups (workers run as Proxmox VMs)
  nodeGroups:
    - name: worker
      replicas: 3
      resources:
        cpu: 4
        memory: 8Gi
        disk: 50Gi
```

This creates:
- **Control plane**: Runs as pods (Kamaji) in management cluster
- **Worker nodes**: Proxmox VMs created via CAPI

### Method 2: Individual Proxmox VMs (Advanced)

Create individual VMs using Cluster API resources:

```yaml
# 1. Define Proxmox cluster connection
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxCluster
metadata:
  name: my-cluster
  namespace: default
spec:
  server: proxmox.example.com:8006
  insecure: false
  controlPlaneEndpoint:
    host: 10.0.0.100  # Load balancer or control plane IP
    port: 6443
---
# 2. Create Cluster API cluster
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-cluster
  namespace: default
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxCluster
    name: my-cluster
  controlPlaneRef:
    apiVersion: controlplane.cluster.x-k8s.io/v1beta1
    kind: KamajiControlPlane
    name: my-cluster
---
# 3. Define VM template
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachineTemplate
metadata:
  name: worker-template
  namespace: default
spec:
  template:
    spec:
      nodeName: pve              # Proxmox node name
      template: ubuntu-22.04-k8s # VM template in Proxmox
      cores: 4
      memory: 8192               # In MB
      diskSize: 50               # In GB
      network:
        default:
          bridge: vmbr0
          model: virtio
---
# 4. Create worker nodes
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: worker-nodes
  namespace: default
spec:
  clusterName: my-cluster
  replicas: 3
  selector:
    matchLabels:
      cluster.x-k8s.io/cluster-name: my-cluster
  template:
    spec:
      clusterName: my-cluster
      version: v1.28.0
      bootstrap:
        configRef:
          apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
          kind: KubeadmConfigTemplate
          name: worker-bootstrap
      infrastructureRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
        kind: ProxmoxMachineTemplate
        name: worker-template
```

### Method 3: Single ProxmoxMachine (Testing)

Create a single VM for testing:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: test-vm-1
  namespace: default
spec:
  nodeName: pve                # Proxmox node
  template: ubuntu-22.04       # VM template
  cores: 2
  memory: 4096                 # MB
  diskSize: 20                 # GB
  network:
    default:
      bridge: vmbr0
      model: virtio
```

## VM Templates in Proxmox

### Create Ubuntu Template

```bash
# On Proxmox host

# 1. Download cloud image
wget https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img

# 2. Create VM
qm create 9000 --name ubuntu-22.04-template --memory 2048 --net0 virtio,bridge=vmbr0

# 3. Import disk
qm importdisk 9000 jammy-server-cloudimg-amd64.img local-lvm

# 4. Attach disk
qm set 9000 --scsihw virtio-scsi-pci --scsi0 local-lvm:vm-9000-disk-0

# 5. Add cloud-init drive
qm set 9000 --ide2 local-lvm:cloudinit

# 6. Set boot order
qm set 9000 --boot c --bootdisk scsi0

# 7. Add serial console
qm set 9000 --serial0 socket --vga serial0

# 8. Enable QEMU agent
qm set 9000 --agent enabled=1

# 9. Convert to template
qm template 9000
```

### Create Kubernetes-ready Template

```bash
# Start from base Ubuntu template
qm clone 9000 9001 --name ubuntu-22.04-k8s

# Start VM
qm start 9001

# SSH into VM and install Kubernetes components
ssh ubuntu@<vm-ip>

# Install containerd
sudo apt update
sudo apt install -y containerd

# Configure containerd
sudo mkdir -p /etc/containerd
containerd config default | sudo tee /etc/containerd/config.toml
sudo systemctl restart containerd
sudo systemctl enable containerd

# Install kubeadm, kubelet, kubectl
sudo apt-get update
sudo apt-get install -y apt-transport-https ca-certificates curl
curl -fsSL https://pkgs.k8s.io/core:/stable:/v1.28/deb/Release.key | sudo gpg --dearmor -o /etc/apt/keyrings/kubernetes-apt-keyring.gpg
echo 'deb [signed-by=/etc/apt/keyrings/kubernetes-apt-keyring.gpg] https://pkgs.k8s.io/core:/stable:/v1.28/deb/ /' | sudo tee /etc/apt/sources.list.d/kubernetes.list
sudo apt-get update
sudo apt-get install -y kubelet kubeadm kubectl
sudo apt-mark hold kubelet kubeadm kubectl

# Disable swap
sudo swapoff -a
sudo sed -i '/ swap / s/^/#/' /etc/fstab

# Clean up
sudo apt clean
sudo cloud-init clean

# Shutdown VM
sudo shutdown -h now

# Convert to template
qm template 9001
```

## Verification

### Check CAPI Provider

```bash
# Check capmox controller
kubectl -n cozy-cluster-api get pods -l control-plane=capmox-controller-manager

# Check logs
kubectl -n cozy-cluster-api logs -l control-plane=capmox-controller-manager
```

### Check ProxmoxCluster

```bash
# List Proxmox clusters
kubectl get proxmoxclusters -A

# Check status
kubectl describe proxmoxcluster <name> -n <namespace>

# Should see status.ready: true
```

### Check ProxmoxMachines

```bash
# List all Proxmox machines
kubectl get proxmoxmachines -A

# Check specific machine
kubectl describe proxmoxmachine <name> -n <namespace>

# Check VM ID in Proxmox
kubectl get proxmoxmachine <name> -n <namespace> -o jsonpath='{.status.vmID}'
```

### Verify in Proxmox

```bash
# On Proxmox host or via Web UI

# List VMs
qm list

# Check VM status
qm status <vmid>

# View VM config
qm config <vmid>

# Check VM console
qm terminal <vmid>
```

## Storage for VMs

### VM Disk Storage

VM disks are created in Proxmox using the configured storage:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: vm-with-storage
spec:
  nodeName: pve
  template: ubuntu-22.04
  diskSize: 100  # 100GB disk in Proxmox storage
  storage: local-lvm  # Proxmox storage pool
```

### Persistent Volumes for Pods

Pods running in VMs can use:

1. **Proxmox CSI** - Direct Proxmox storage:
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
spec:
  storageClassName: proxmox-data-xfs
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

2. **LINSTOR** - Replicated storage:
```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-pvc
spec:
  storageClassName: replicated
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
```

## Networking

### VM Network Configuration

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: vm-custom-network
spec:
  nodeName: pve
  template: ubuntu-22.04
  network:
    default:
      bridge: vmbr0    # Proxmox bridge
      model: virtio    # Network card model
      vlan: 100        # Optional VLAN tag
```

### Load Balancer (MetalLB)

Services of type `LoadBalancer` get external IPs from MetalLB:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  type: LoadBalancer
  ports:
    - port: 80
      targetPort: 8080
  selector:
    app: my-app
```

## Scaling

### Scale Workers

```bash
# Scale MachineDeployment
kubectl scale machinedeployment worker-nodes --replicas=5 -n default

# Or patch the Kubernetes cluster
kubectl patch kubernetes tenant-cluster -n tenant-demo --type merge -p '{"spec":{"nodeGroups":[{"name":"worker","replicas":5}]}}'
```

### Auto-scaling (Future)

With Cluster API autoscaler:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: MachineDeployment
metadata:
  name: worker-nodes
  annotations:
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-min-size: "1"
    cluster.x-k8s.io/cluster-api-autoscaler-node-group-max-size: "10"
spec:
  replicas: 3
  # ...
```

## Troubleshooting

### VM Not Creating

```bash
# 1. Check ProxmoxMachine status
kubectl describe proxmoxmachine <name> -n <namespace>

# 2. Check CAPI controller logs
kubectl -n cozy-cluster-api logs -l control-plane=capmox-controller-manager --tail=100

# 3. Check Proxmox API access
curl -k https://proxmox-host:8006/api2/json/version \
  -H "Authorization: PVEAPIToken=<token-id>=<token-secret>"

# 4. Check Proxmox task log
# In Proxmox Web UI: Datacenter → Tasks
# Or via CLI:
pvesh get /cluster/tasks
```

### VM Stuck in Provisioning

```bash
# Check if VM exists in Proxmox
qm list | grep <name>

# Check VM status
qm status <vmid>

# Try starting VM manually
qm start <vmid>

# Check VM console for boot issues
qm terminal <vmid>
```

### VM Not Joining Cluster

```bash
# 1. Check cloud-init logs on VM
ssh ubuntu@<vm-ip>
sudo cloud-init status --long
sudo journalctl -u cloud-init

# 2. Check kubelet status
sudo systemctl status kubelet
sudo journalctl -u kubelet

# 3. Check network connectivity
ping <api-server-ip>
curl -k https://<api-server-ip>:6443

# 4. Check bootstrap kubeconfig
sudo cat /etc/kubernetes/bootstrap-kubelet.conf
```

## Best Practices

1. **Use VM templates** - Faster provisioning, consistent configuration
2. **Cloud-init** - Automate VM initialization
3. **Resource requests** - Set appropriate CPU/memory/disk sizes
4. **Storage planning** - Choose correct Proxmox storage pool
5. **Network isolation** - Use VLANs for tenant separation
6. **Monitoring** - Monitor both Proxmox and Kubernetes metrics
7. **Backups** - Regular backups of VM templates and configurations
8. **Testing** - Test failover and recovery procedures

## Examples

### Example 1: Development Cluster

Small cluster for development:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: dev-cluster
  namespace: tenant-dev
spec:
  replicas: 1  # Single control plane
  nodeGroups:
    - name: worker
      replicas: 2  # Two workers
      resources:
        cpu: 2
        memory: 4Gi
        disk: 30Gi
```

### Example 2: Production Cluster

HA cluster for production:

```yaml
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: prod-cluster
  namespace: tenant-prod
spec:
  replicas: 3  # HA control plane
  nodeGroups:
    - name: worker
      replicas: 5
      resources:
        cpu: 8
        memory: 32Gi
        disk: 100Gi
    - name: worker-gpu
      replicas: 2
      resources:
        cpu: 16
        memory: 64Gi
        disk: 200Gi
```

### Example 3: Database VM

Dedicated VM for database:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: postgres-vm
  namespace: tenant-db
spec:
  nodeName: pve
  template: ubuntu-22.04
  cores: 8
  memory: 32768  # 32GB
  diskSize: 500  # 500GB for database
  storage: local-ssd  # Use SSD storage
  network:
    default:
      bridge: vmbr0
      vlan: 200  # Database VLAN
```

## API Access

### Using kubectl

```bash
# List all VMs (ProxmoxMachines)
kubectl get proxmoxmachines -A

# Get VM details
kubectl get proxmoxmachine <name> -n <namespace> -o yaml

# Delete VM
kubectl delete proxmoxmachine <name> -n <namespace>
```

### Using API Client (Python)

```python
from kubernetes import client, config

# Load kubeconfig
config.load_kube_config()

# Create custom objects API
api = client.CustomObjectsApi()

# Create ProxmoxMachine
vm = {
    "apiVersion": "infrastructure.cluster.x-k8s.io/v1beta1",
    "kind": "ProxmoxMachine",
    "metadata": {
        "name": "api-vm",
        "namespace": "default"
    },
    "spec": {
        "nodeName": "pve",
        "template": "ubuntu-22.04",
        "cores": 4,
        "memory": 8192,
        "diskSize": 50
    }
}

api.create_namespaced_custom_object(
    group="infrastructure.cluster.x-k8s.io",
    version="v1beta1",
    namespace="default",
    plural="proxmoxmachines",
    body=vm
)
```

## Migration from KubeVirt

If you're migrating from KubeVirt-based setup:

1. **VirtualMachine CRD** → **ProxmoxMachine CRD**
2. **VMI** → Proxmox VM (native)
3. **DataVolume** → Proxmox disk
4. **virt-launcher pods** → No equivalent (VMs run in Proxmox)

**Key difference**: No pods involved. VMs run directly in Proxmox hypervisor.

## References

- [Cluster API Proxmox Provider](https://github.com/ionos-cloud/cluster-api-provider-proxmox)
- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [Proxmox VE Documentation](https://pve.proxmox.com/pve-docs/)
- [Cloud-init Documentation](https://cloudinit.readthedocs.io/)
- [Kamaji](https://github.com/clastix/kamaji)

