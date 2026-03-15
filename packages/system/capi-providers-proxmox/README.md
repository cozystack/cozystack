# CozyStack CAPI Proxmox Provider

This Helm chart provides the Cluster API Infrastructure Provider for Proxmox VE, allowing you to manage Proxmox VMs as Kubernetes nodes through Cluster API.

## Overview

The CozyStack CAPI Proxmox Provider integrates with the Cluster API Operator to provide:

- **Proxmox VM Management**: Create, configure, and manage VMs in Proxmox VE
- **Kubernetes Node Integration**: VMs become Kubernetes worker nodes
- **Infrastructure as Code**: Declarative management of Proxmox infrastructure
- **Operator Pattern**: Kubernetes-native management using operators

## Architecture

```
┌─────────────────┐    ┌──────────────────┐    ┌─────────────────┐
│   Proxmox VE    │    │  Cluster API     │    │  Kubernetes     │
│                 │    │  Operator        │    │  Management     │
│ ┌─────────────┐ │    │                  │    │  Cluster        │
│ │ VM Template │ │◄───┤ ┌──────────────┐ │◄───┤                 │
│ └─────────────┘ │    │ │ Infrastructure│ │    │ ┌─────────────┐ │
│                 │    │ │ Provider      │ │    │ │ Cluster     │ │
│ ┌─────────────┐ │    │ │ (Proxmox)     │ │    │ │ Resources   │ │
│ │ Storage     │ │◄───┤ └──────────────┘ │    │ └─────────────┘ │
│ │ Backend     │ │    │                  │    │                 │
│ └─────────────┘ │    │ ┌──────────────┐ │    │ ┌─────────────┐ │
└─────────────────┘    │ │ Core         │ │    │ │ Machine     │ │
                       │ │ Provider     │ │    │ │ Resources   │ │
                       │ └──────────────┘ │    │ └─────────────┘ │
                       └──────────────────┘    └─────────────────┘
```

## Prerequisites

- Kubernetes cluster with Cluster API Operator installed
- Proxmox VE server
- Access to Proxmox API
- `kubectl` CLI tool

## Installation

1. **Enable Proxmox provider in values**:
```yaml
# In capi-providers-infraprovider values.yaml
providers:
  kubevirt: false
  proxmox: true
```

2. **Install the chart**:
```bash
helm install cozy-capi-providers-proxmox ./packages/system/capi-providers-proxmox
```

3. **Verify installation**:
```bash
kubectl get infrastructureproviders
kubectl get pods -n cozy-cluster-api
```

## Configuration

### Infrastructure Provider

The chart creates an InfrastructureProvider resource:

```yaml
apiVersion: operator.cluster.x-k8s.io/v1alpha2
kind: InfrastructureProvider
metadata:
  name: proxmox
spec:
  version: v0.6.2-infraprovider
  fetchConfig:
    selector:
      matchLabels:
        infraprovider-components: cozy
```

### Proxmox Configuration

Configure Proxmox connection in your cluster resources:

```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxCluster
metadata:
  name: my-proxmox-cluster
spec:
  server: proxmox.example.com
  insecure: false
  controlPlaneEndpoint:
    host: load-balancer.example.com
    port: 6443
```

## Usage Examples

### Create a Proxmox Cluster

1. **Create cluster configuration**:
```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Cluster
metadata:
  name: proxmox-cluster
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxCluster
    name: proxmox-cluster
```

2. **Create Proxmox infrastructure**:
```yaml
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxCluster
metadata:
  name: proxmox-cluster
spec:
  server: proxmox.example.com
  controlPlaneEndpoint:
    host: load-balancer.example.com
    port: 6443
```

3. **Create machines**:
```yaml
apiVersion: cluster.x-k8s.io/v1beta1
kind: Machine
metadata:
  name: proxmox-worker
spec:
  infrastructureRef:
    apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
    kind: ProxmoxMachine
    name: proxmox-worker
---
apiVersion: infrastructure.cluster.x-k8s.io/v1beta1
kind: ProxmoxMachine
metadata:
  name: proxmox-worker
spec:
  nodeName: proxmox-node-1
  template: ubuntu-2004-template
  cores: 2
  memory: 4096
  diskSize: 20
```

## Integration with CozyStack

This provider integrates seamlessly with the CozyStack platform:

- **CozyStack API**: Managed through CozyStack's API
- **UI Integration**: Available in CozyStack dashboard
- **Multi-tenancy**: Supports tenant isolation
- **Resource Management**: Integrated with CozyStack's resource management

## Troubleshooting

### Common Issues

1. **Provider Not Starting**:
```bash
kubectl logs -n cozy-cluster-api -l app.kubernetes.io/name=cozy-capi-providers-proxmox
```

2. **VM Creation Fails**:
```bash
kubectl describe proxmoxmachine proxmox-worker
kubectl describe proxmoxcluster proxmox-cluster
```

3. **Connection Issues**:
```bash
kubectl get secrets -n cozy-cluster-api
kubectl describe secret proxmox-credentials
```

### Debug Commands

```bash
# Check provider status
kubectl get infrastructureproviders
kubectl describe infrastructureprovider proxmox

# Check cluster resources
kubectl get clusters
kubectl get proxmoxclusters
kubectl get proxmoxmachines

# Check events
kubectl get events --field-selector involvedObject.kind=ProxmoxCluster
kubectl get events --field-selector involvedObject.kind=ProxmoxMachine
```

## Uninstallation

```bash
helm uninstall cozy-capi-providers-proxmox
```

## Support

- **Documentation**: https://docs.cozystack.io
- **GitHub Issues**: https://github.com/your-org/cozystack/issues
- **Community**: https://community.cozystack.io
