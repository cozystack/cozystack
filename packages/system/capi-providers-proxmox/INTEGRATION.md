# Proxmox Integration with CozyStack CAPI

This document describes the integration of ionos-cloud/cluster-api-provider-proxmox with CozyStack's Cluster API infrastructure, following the same patterns as kubevirt integration.

## Architecture Overview

The integration follows the established CozyStack CAPI pattern:

```
┌─────────────────────────────────────────────────────────────────┐
│                    CozyStack CAPI Architecture                  │
├─────────────────────────────────────────────────────────────────┤
│  capi-operator                                                  │
│  ├── cozy-capi-providers-core (Core Provider)                  │
│  ├── cozy-capi-providers-bootstrap (Bootstrap Provider)        │
│  ├── cozy-capi-providers-cpprovider (Control Plane Provider)   │
│  └── cozy-capi-providers-infraprovider (Infrastructure Provider)│
│      ├── kubevirt (existing)                                   │
│      └── proxmox (new)                                         │
└─────────────────────────────────────────────────────────────────┘
```

## Implementation Details

### 1. Chart Structure

The `cozy-capi-providers-proxmox` chart follows the same structure as other CAPI provider charts:

```
packages/system/capi-providers-proxmox/
├── Chart.yaml                    # Chart metadata
├── Makefile                      # Build configuration
├── README.md                     # Documentation
├── INTEGRATION.md               # This file
├── templates/
│   ├── providers.yaml           # InfrastructureProvider resource
│   └── configmaps.yaml          # Component configuration
├── examples/
│   └── proxmox-cluster.yaml     # Example cluster configuration
└── scripts/
    └── test-proxmox-cluster.sh  # Test script
```

### 2. InfrastructureProvider Configuration

The chart creates an InfrastructureProvider resource for Proxmox:

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

### 3. Integration with capi-providers-infraprovider

The main `capi-providers-infraprovider` chart has been updated to support both kubevirt and proxmox:

```yaml
# values.yaml
providers:
  kubevirt: true
  proxmox: false  # Enable to use Proxmox instead of KubeVirt
```

The templates now conditionally create providers based on the values:

```yaml
{{- if .Values.providers.kubevirt }}
---
apiVersion: operator.cluster.x-k8s.io/v1alpha2
kind: InfrastructureProvider
metadata:
  name: kubevirt
spec:
  version: v0.1.10-infraprovider
  # ... kubevirt configuration
{{- end }}

{{- if .Values.providers.proxmox }}
---
apiVersion: operator.cluster.x-k8s.io/v1alpha2
kind: InfrastructureProvider
metadata:
  name: proxmox
spec:
  version: v0.6.2-infraprovider
  # ... proxmox configuration
{{- end }}
```

## Usage Patterns

### 1. Enabling Proxmox Provider

To use Proxmox instead of KubeVirt:

```yaml
# In capi-providers-infraprovider values
providers:
  kubevirt: false
  proxmox: true
```

### 2. Creating Proxmox Clusters

Use the standard Cluster API patterns with Proxmox-specific resources:

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
  server: proxmox.example.com
  controlPlaneEndpoint:
    host: load-balancer.example.com
    port: 6443
```

### 3. Creating Proxmox Machines

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

## Migration from KubeVirt

To migrate from KubeVirt to Proxmox:

1. **Update values.yaml**:
```yaml
providers:
  kubevirt: false
  proxmox: true
```

2. **Update cluster resources**:
   - Change `KubevirtCluster` to `ProxmoxCluster`
   - Change `KubevirtMachine` to `ProxmoxMachine`
   - Update spec fields to match Proxmox API

3. **Update credentials**:
   - Replace KubeVirt credentials with Proxmox credentials
   - Update connection details (server, port, etc.)

## Testing

Use the provided test script to verify the integration:

```bash
# Basic checks
./scripts/test-proxmox-cluster.sh

# Full cluster creation test
./scripts/test-proxmox-cluster.sh --test-cluster
```

## Troubleshooting

### Common Issues

1. **Provider Not Installed**:
```bash
kubectl get infrastructureproviders
kubectl describe infrastructureprovider proxmox
```

2. **VM Creation Fails**:
```bash
kubectl describe proxmoxmachine <machine-name>
kubectl describe proxmoxcluster <cluster-name>
```

3. **Connection Issues**:
```bash
kubectl get secrets
kubectl describe secret proxmox-credentials
```

### Debug Commands

```bash
# Check provider status
kubectl get infrastructureproviders
kubectl get pods -n cozy-cluster-api

# Check cluster resources
kubectl get clusters
kubectl get proxmoxclusters
kubectl get proxmoxmachines

# Check events
kubectl get events --field-selector involvedObject.kind=ProxmoxCluster
kubectl get events --field-selector involvedObject.kind=ProxmoxMachine
```

## Future Enhancements

1. **Multi-Provider Support**: Support for multiple infrastructure providers simultaneously
2. **Provider Switching**: Dynamic switching between providers
3. **Advanced Configuration**: More granular configuration options
4. **Monitoring Integration**: Enhanced monitoring and observability
5. **Backup and Recovery**: Integrated backup and recovery solutions

## References

- [Cluster API Documentation](https://cluster-api.sigs.k8s.io/)
- [ionos-cloud/cluster-api-provider-proxmox](https://github.com/ionos-cloud/cluster-api-provider-proxmox)
- [CozyStack Documentation](https://docs.cozystack.io)
- [Proxmox VE Documentation](https://pve.proxmox.com/wiki/Main_Page)
