# flux-instance

![Version: 0.27.0](https://img.shields.io/badge/Version-0.27.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: v0.27.0](https://img.shields.io/badge/AppVersion-v0.27.0-informational?style=flat-square)

This chart is a thin wrapper around the `FluxInstance` custom resource, which is
used by the [Flux Operator](https://github.com/controlplaneio-fluxcd/flux-operator)
to install, configure and automatically upgrade Flux.

## Prerequisites

- Kubernetes 1.22+
- Helm 3.8+

## Installing the Chart

To deploy Flux in the `flux-system` namespace:

```console
helm -n flux-system install flux oci://ghcr.io/controlplaneio-fluxcd/charts/flux-instance
```

For more information on the available configuration options,
see the [Flux Instance documentation](https://fluxcd.control-plane.io/operator/fluxinstance/).

## Uninstalling the Chart

To uninstall Flux without affecting the resources it manages:

```console
helm -n flux-system uninstall flux
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| commonAnnotations | object | `{}` | Common annotations to add to all deployed objects including pods. |
| commonLabels | object | `{}` | Common labels to add to all deployed objects including pods. |
| fullnameOverride | string | `"flux"` |  |
| healthcheck.enabled | bool | `false` | Enable post-install and post-upgrade health checks. |
| healthcheck.timeout | string | `"5m"` | Health check timeout in Go duration format. |
| instance.cluster | object | `{"domain":"cluster.local","multitenant":false,"networkPolicy":true,"size":"","tenantDefaultServiceAccount":"default","type":"kubernetes"}` | Cluster https://fluxcd.control-plane.io/operator/fluxinstance/#cluster-configuration |
| instance.commonMetadata | object | `{"annotations":{},"labels":{}}` | Common metadata https://fluxcd.control-plane.io/operator/fluxinstance/#common-metadata |
| instance.components | list | `["source-controller","kustomize-controller","helm-controller","notification-controller"]` | Components https://fluxcd.control-plane.io/operator/fluxinstance/#components-configuration |
| instance.distribution | object | `{"artifact":"oci://ghcr.io/controlplaneio-fluxcd/flux-operator-manifests:latest","artifactPullSecret":"","imagePullSecret":"","registry":"ghcr.io/fluxcd","version":"2.x"}` | Distribution https://fluxcd.control-plane.io/operator/fluxinstance/#distribution-configuration |
| instance.kustomize.patches | list | `[]` | Kustomize patches https://fluxcd.control-plane.io/operator/fluxinstance/#kustomize-patches |
| instance.sharding | object | `{"key":"sharding.fluxcd.io/key","shards":[]}` | Sharding https://fluxcd.control-plane.io/operator/fluxinstance/#sharding-configuration |
| instance.storage | object | `{"class":"","size":""}` | Storage https://fluxcd.control-plane.io/operator/fluxinstance/#storage-configuration |
| instance.sync | object | `{"interval":"1m","kind":"GitRepository","name":"","path":"","provider":"","pullSecret":"","ref":"","url":""}` | Sync https://fluxcd.control-plane.io/operator/fluxinstance/#sync-configuration |
| nameOverride | string | `""` |  |

## Source Code

* <https://github.com/controlplaneio-fluxcd/flux-operator>
* <https://github.com/controlplaneio-fluxcd/charts>
