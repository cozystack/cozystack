# Proxmox Worker Helm Chart

A Helm chart for adding Proxmox server as Kubernetes worker node via kubeadm.

## Overview

This chart manages the process of adding a Proxmox server as a worker node to an existing Kubernetes cluster. It handles the installation and configuration of containerd, kubelet, and other necessary components, then joins the Proxmox server to the cluster using kubeadm.

## Architecture

```
┌─────────────────┐    ┌──────────────────┐
│   Proxmox VE    │    │  Kubernetes      │
│                 │    │  Cluster         │
│ ┌─────────────┐ │    │                  │
│ │ Containerd  │ │◄───┤ ┌──────────────┐ │
│ │ Runtime     │ │    │ │ Control      │ │
│ └─────────────┘ │    │ │ Plane        │ │
│                 │    │ └──────────────┘ │
│ ┌─────────────┐ │    │                  │
│ │ Kubelet     │ │◄───┤ ┌──────────────┐ │
│ │ Agent       │ │    │ │ API Server   │ │
│ └─────────────┘ │    │ └──────────────┘ │
│                 │    │                  │
│ ┌─────────────┐ │    │ ┌──────────────┐ │
│ │ CNI Plugin  │ │◄───┤ │ Networking   │ │
│ │ (Cilium)    │ │    │ │ (Cilium)     │ │
│ └─────────────┘ │    │ └──────────────┘ │
└─────────────────┘    └──────────────────┘
```

## Features

- **Automated Setup**: Automatically installs and configures containerd, kubelet, and other required components
- **Kubeadm Integration**: Uses kubeadm to join the Proxmox server to the Kubernetes cluster
- **Security**: Implements RBAC, network policies, and pod security standards
- **Monitoring**: Includes node exporter and kubelet metrics collection
- **Flexible Configuration**: Supports custom networking, resource limits, and runtime configurations
- **Pre/Post Checks**: Validates system requirements and verifies installation

## Prerequisites

- Proxmox VE server with Ubuntu 22.04 or compatible OS
- Existing Kubernetes cluster with control plane accessible
- Network connectivity between Proxmox server and Kubernetes cluster
- Sufficient resources (minimum 2 CPU cores, 2GB RAM, 20GB disk)

## Quick Start

1. **Prepare Proxmox Server**:
   ```bash
   # Run the setup script on Proxmox server
   ./scripts/setup-proxmox.sh -h proxmox.example.com -p your-password
   ```

2. **Install the Chart**:
   ```bash
   # Install with default values
   helm install proxmox-worker . -n proxmox-worker --create-namespace
   
   # Or install with custom values
   helm install proxmox-worker . -n proxmox-worker --create-namespace -f examples/example-values.yaml
   ```

3. **Verify Installation**:
   ```bash
   # Check if the node is ready
   kubectl get nodes
   
   # Check pod status
   kubectl get pods -n proxmox-worker
   ```

## Configuration

### Proxmox Configuration

```yaml
proxmox:
  server:
    host: "proxmox.example.com"
    port: 8006
    insecure: false
  credentials:
    secretName: "proxmox-credentials"
    username: "root@pam"
    password: "your-password"
```

### Kubernetes Configuration

```yaml
kubernetes:
  controlPlaneEndpoint: "k8s-api.example.com:6443"
  clusterName: "cozystack"
  kubeadm:
    token: "your-join-token"
    caCertHash: "sha256:your-ca-cert-hash"
    joinConfig:
      nodeName: "proxmox-worker"
      labels:
        node-role.kubernetes.io/worker: ""
        node.kubernetes.io/instance-type: "proxmox"
```

### Networking Configuration

```yaml
networking:
  cni: "cilium"
  podCIDR: "10.244.0.0/16"
  serviceCIDR: "10.96.0.0/12"
  dns:
    domain: "cluster.local"
    nameservers:
      - "8.8.8.8"
      - "8.8.4.4"
```

## Usage Examples

### Basic Installation

```bash
# Install with minimal configuration
helm install proxmox-worker . \
  --set proxmox.server.host=proxmox.example.com \
  --set proxmox.credentials.password=your-password \
  --set kubernetes.controlPlaneEndpoint=k8s-api.example.com:6443 \
  --set kubernetes.kubeadm.token=your-token \
  --set kubernetes.kubeadm.caCertHash=sha256:your-hash
```

### Production Installation

```bash
# Install with production values
helm install proxmox-worker . \
  --namespace proxmox-worker \
  --create-namespace \
  --values examples/example-values.yaml \
  --wait \
  --timeout 10m
```

### Custom Resource Limits

```yaml
resources:
  kubelet:
    requests:
      cpu: "500m"
      memory: "512Mi"
    limits:
      cpu: "2000m"
      memory: "2Gi"
```

## Monitoring

The chart includes monitoring capabilities:

- **Node Exporter**: Collects system metrics from the Proxmox server
- **Kubelet Metrics**: Exposes Kubernetes node metrics
- **Health Checks**: Validates node readiness and pod scheduling

### Accessing Metrics

```bash
# Node exporter metrics
kubectl port-forward -n proxmox-worker svc/proxmox-worker-node-exporter 9100:9100

# Kubelet metrics
kubectl port-forward -n proxmox-worker svc/proxmox-worker-kubelet-metrics 10250:10250
```

## Troubleshooting

### Common Issues

1. **Node Not Joining Cluster**:
   - Check network connectivity between Proxmox and Kubernetes cluster
   - Verify token and CA certificate hash are correct
   - Check kubelet logs: `journalctl -u kubelet`

2. **Containerd Issues**:
   - Verify containerd is running: `systemctl status containerd`
   - Check containerd logs: `journalctl -u containerd`

3. **Resource Issues**:
   - Ensure sufficient CPU, memory, and disk space
   - Check resource limits in values.yaml

### Debug Commands

```bash
# Check node status
kubectl describe node proxmox-worker

# Check pod logs
kubectl logs -n proxmox-worker -l app.kubernetes.io/name=proxmox-worker

# Check system resources
kubectl top node proxmox-worker

# Check network connectivity
kubectl exec -n proxmox-worker -it <pod-name> -- ping k8s-api.example.com
```

## Security Considerations

- Use strong passwords and secure tokens
- Enable network policies for additional security
- Regularly rotate join tokens and certificates
- Monitor for security updates and apply them promptly
- Use RBAC to limit access to Proxmox resources

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## License

This chart is licensed under the Apache 2.0 License.

## Support

For support and questions:
- Create an issue in the repository
- Check the troubleshooting section
- Review the documentation
