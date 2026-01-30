# Proxmox VE Helm Chart

A basic Helm chart for Proxmox VE integration with Kubernetes.

## Overview

This Helm chart provides basic Proxmox VE integration components:

- **Basic Proxmox Configuration** - Proxmox server connection settings
- **Network Policies** - Basic security policies for Proxmox components
- **RBAC Configuration** - Role-based access control

> **Note**: All major components (Cluster API, CSI, Networking, Monitoring) are managed by the CozyStack platform itself and are not included in this chart.

## Architecture

```
┌─────────────────┐    ┌──────────────────┐
│   Proxmox VE    │    │  CozyStack       │
│                 │    │  Platform        │
│ ┌─────────────┐ │    │                  │
│ │ VM Template │ │◄───┤ ┌──────────────┐ │
│ └─────────────┘ │    │ │ Cluster API  │ │
│                 │    │ │ Provider     │ │
│ ┌─────────────┐ │    │ └──────────────┘ │
│ │ Storage     │ │◄───┤                  │
│ │ Backend     │ │    │ ┌──────────────┐ │
│ └─────────────┘ │    │ │ CSI Driver   │ │
└─────────────────┘    │ └──────────────┘ │
                       │                  │
                       │ ┌──────────────┐ │
                       │ │ Networking   │ │
                       │ │ (Kube-OVN +  │ │
                       │ │  Cilium)     │ │
                       │ └──────────────┘ │
                       │                  │
                       │ ┌──────────────┐ │
                       │ │ Monitoring   │ │
                       │ │ (Prometheus +│ │
                       │ │  Grafana)    │ │
                       │ └──────────────┘ │
                       └──────────────────┘
```

## Features

- **Basic Proxmox Configuration**: Proxmox server connection settings
- **Network Policies**: Basic security policies for Proxmox components
- **RBAC Configuration**: Role-based access control
- **CozyStack Integration**: Seamless integration with CozyStack platform
- **Lightweight**: Minimal footprint with only essential components

## Prerequisites

- Kubernetes cluster
- Proxmox VE server
- `helm` CLI tool (v3.0+)
- `kubectl` CLI tool
- CozyStack platform installed

## Quick Start

1. **Configure values**:
```bash
cp examples/example-values.yaml my-values.yaml
# Edit my-values.yaml with your Proxmox configuration
```

2. **Install using the script**:
```bash
./scripts/install.sh --values my-values.yaml
```

3. **Or install manually**:
```bash
helm install proxmox-ve . \
  --namespace proxmox-ve \
  --create-namespace \
  --values my-values.yaml
```

## Configuration

### Basic Proxmox Configuration

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

### Security Configuration

```yaml
security:
  networkPolicies:
    enabled: true
  rbac:
    enabled: true
```

### Resource Configuration

```yaml
resources:
  default:
    requests:
      cpu: "100m"
      memory: "128Mi"
    limits:
      cpu: "500m"
      memory: "512Mi"
```

### CozyStack Integration

All major components (Cluster API, CSI, Networking, Monitoring) are managed by the CozyStack platform. This chart provides only basic Proxmox configuration and security policies.

## Usage Examples

### Basic Installation

```bash
# Install the chart
helm install proxmox-ve . \
  --namespace proxmox-ve \
  --create-namespace \
  --values my-values.yaml
```

### Check Installation

```bash
# Check network policies
kubectl get networkpolicies

# Check RBAC
kubectl get clusterroles
kubectl get clusterrolebindings
```

### CozyStack Platform Integration

All major Proxmox functionality (Cluster API, CSI, Networking, Monitoring) is managed through the CozyStack platform. This chart provides only basic configuration and security policies.

## Monitoring

Monitoring is handled by the CozyStack platform:

- **Prometheus**: Metrics collection
- **Grafana**: Visualization dashboards
- **Cilium Hubble**: Network observability

### Access Monitoring

```bash
# Access through CozyStack platform
# All monitoring components are managed by CozyStack
```

## Troubleshooting

### Common Issues

1. **Network Policies Not Applied**:
   - Check if network policies are enabled in values
   - Verify CNI supports network policies
   - Review network policy logs

2. **RBAC Issues**:
   - Check cluster roles and bindings
   - Verify service account permissions
   - Review RBAC configuration

### Debug Commands

```bash
# Check network policies
kubectl get networkpolicies

# Check RBAC
kubectl get clusterroles
kubectl get clusterrolebindings

# Check CozyStack integration
kubectl get pods -n cozystack-system
```

## Uninstallation

```bash
# Uninstall using Helm
helm uninstall proxmox-ve --namespace proxmox-ve

# Or using the script
./scripts/install.sh --uninstall
```

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Support

- **Documentation**: https://docs.cozystack.io
- **GitHub Issues**: https://github.com/your-org/cozystack/issues
- **Community**: https://community.cozystack.io
- **Slack**: https://cozystack.slack.com
