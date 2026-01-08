# local-ccm Helm Chart

Local Cloud Controller Manager for Kubernetes - automatically detects and manages node IP addresses.

## Features

- Automatic node IP address detection using routing table
- Support for both internal and external IP detection
- Automatic removal of cloud provider initialization taint
- Minimal resource footprint
- Runs as DaemonSet on all nodes

## Installation

### Quick Start

Install with default configuration:

```bash
helm install local-ccm ./charts/local-ccm --namespace kube-system
```

### Custom Configuration

Create a `values.yaml` file:

```yaml
ipDetection:
  externalIPTarget: "1.1.1.1"
  internalIPTarget: "10.0.0.1"

controller:
  verbosity: 3
```

Install with custom values:

```bash
helm install local-ccm ./charts/local-ccm \
  --namespace kube-system \
  --values values.yaml
```

### Inline Configuration

```bash
helm install local-ccm ./charts/local-ccm \
  --namespace kube-system \
  --set ipDetection.externalIPTarget=1.1.1.1 \
  --set ipDetection.internalIPTarget=10.0.0.1
```

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Container image repository | `ghcr.io/cozystack/local-ccm` |
| `image.tag` | Container image tag | `v0.1.0` |
| `image.pullPolicy` | Image pull policy | `Always` |
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `local-ccm` |
| `ipDetection.externalIPTarget` | Target IP for external IP detection | `8.8.8.8` |
| `ipDetection.internalIPTarget` | Target IP for internal IP detection (empty = disabled) | `""` |
| `controller.removeTaint` | Remove uninitialized taint | `true` |
| `controller.reconcileInterval` | Reconciliation interval | `10s` |
| `controller.verbosity` | Log verbosity level (0-5) | `2` |
| `resources.requests.cpu` | CPU resource requests | `10m` |
| `resources.requests.memory` | Memory resource requests | `32Mi` |
| `resources.limits.cpu` | CPU resource limits | `100m` |
| `resources.limits.memory` | Memory resource limits | `64Mi` |
| `tolerations` | Pod tolerations | `[{operator: Exists}]` |
| `affinity` | Pod affinity rules | See values.yaml |
| `labels` | Additional labels for all resources | `{}` |
| `podAnnotations` | Additional pod annotations | `{}` |

## Uninstallation

```bash
helm uninstall local-ccm --namespace kube-system
```

## Upgrading

```bash
helm upgrade local-ccm ./charts/local-ccm \
  --namespace kube-system \
  --values values.yaml
```

## Requirements

- Kubernetes 1.19+
- Helm 3.0+

## License

Licensed under the Apache License, Version 2.0
