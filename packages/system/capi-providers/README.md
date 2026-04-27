# Cluster API Providers

This directory contains configurations for various Cluster API providers.

## Proxmox Integration

### Requirements
- Kubernetes cluster
- Proxmox VE server
- Access to Proxmox API
- Installed Cluster API

### Configuration

1. Enable Proxmox provider in your configuration:

```yaml
providers:
  proxmox: true
```

2. Ensure you have the necessary secrets for Proxmox access:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: proxmox-credentials
  namespace: default
type: Opaque
stringData:
  username: your-proxmox-username
  password: your-proxmox-password
  url: https://your-proxmox-server:8006/api2/json
```

### Usage

1. Create a cluster:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: my-proxmox-cluster
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxCluster
    name: my-proxmox-cluster
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxCluster
metadata:
  name: my-proxmox-cluster
spec:
  server: your-proxmox-server
  insecure: false
  controlPlaneEndpoint:
    host: your-load-balancer-host
    port: 6443
```

2. Create a machine:

```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name: my-proxmox-machine
spec:
  bootstrap:
    configRef:
      apiVersion: bootstrap.cluster.x-k8s.io/v1beta1
      kind: KubeadmConfig
      name: my-proxmox-machine
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxMachine
    name: my-proxmox-machine
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: my-proxmox-machine
spec:
  nodeName: your-proxmox-node
  template: ubuntu-2004-template
  cores: 2
  memory: 4096
  diskSize: 20
```

### Debugging

The project includes two scripts for debugging Proxmox integration:

1. `create-proxmox-cluster.sh`:
   - Creates a cluster with built-in debugging capabilities
   - Checks provider status
   - Monitors cluster creation progress
   - Provides detailed logs on failure

2. `debug-proxmox-cluster.sh`:
   - Interactive debugging menu
   - Color-coded output
   - Comprehensive checks for:
     - Cluster API providers status
     - Cluster resources
     - Provider logs
     - Machine logs
     - Proxmox connection
     - Cluster events

#### Debugging Commands

1. Check provider status:
```bash
kubectl get pods -n capi-proxmox-system
```

2. Check provider logs:
```bash
kubectl logs -n capi-proxmox-system -l control-plane=controller-manager
```

3. Check machine status:
```bash
kubectl get machines -A
```

4. Check events:
```bash
kubectl get events --field-selector involvedObject.kind=ProxmoxMachine
```

5. Check Proxmox connection:
```bash
kubectl get secret proxmox-credentials
```

#### Common Issues and Solutions

1. Provider Pod Issues:
   - Check if the pod is running: `kubectl get pods -n capi-proxmox-system`
   - Check pod logs: `kubectl logs -n capi-proxmox-system <pod-name>`
   - Verify Proxmox credentials in the secret

2. Machine Creation Issues:
   - Check machine status: `kubectl get machines -A`
   - Check Proxmox machine status: `kubectl get proxmoxmachines -A`
   - Verify VM template exists in Proxmox

3. Connection Issues:
   - Verify Proxmox URL is accessible
   - Check credentials in the secret
   - Ensure Proxmox API is enabled and accessible

### Known Limitations
- Only Linux systems are supported
- A pre-created VM template is required
- Only qemu/kvm virtual machines are supported

### Additional Information
- [Official cluster-api-provider-proxmox documentation](https://github.com/ionos-cloud/cluster-api-provider-proxmox)
- [Cluster API documentation](https://cluster-api.sigs.k8s.io/) 