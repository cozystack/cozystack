# OpenShift Console (Bridge)

[Bridge](https://github.com/openshift/console) is the OpenShift console.

## TL;DR

```console
$ helm repo add av1o https://av1o.gitlab.io/charts
$ helm install bridge av1o/openshift-console
```

## Introduction

This chart bootstraps a deployment of the OpenShift Console on a [Kubernetes](https://kubernetes.io) cluster using the [Helm](https://helm.sh) package manager.

The OpenShift Console is designed for running on OpenShift, however it works perfectly fine in native Kubernetes. Since the Console is unable to use the default OpenShift OAuth2, this chart is expecting a Dex deployment which is configured to generate OIDC tokens for the Kubernetes API server.
This behaviour can be configured with the `extraEnv` map.

## Prerequisites

- Kubernetes 1.12+
- Helm 3

## Installing the Chart
To install the chart with the release name `my-release`:

```console
$ helm install my-release av1o/openshift-console
```

The command deploys the console on the Kubernetes cluster in the default configuration.

> **Tip**: List all releases using `helm list`

## Uninstalling the Chart

To uninstall/delete the `my-release` deployment:

```console
$ helm delete my-release
```

## Parameters

The following table lists the configurable parameters of the OpenShift Console chart and their default values.

| Parameter                      | Description                                                                                                                                    | Default                       |
|--------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------|-------------------------------|
| `replicaCount`                 | Number of pods to run                                                                                                                          | 1                             |
| `image.registry`               | Docker image registry                                                                                                                          | `quay.io`                     |
| `image.repository`             | Docker image name                                                                                                                              | `openshift/origin-console`    |
| `image.pullPolicy`             | Docker image pull policy                                                                                                                       | `IfNotPresent`                |
| `image.tag`                    | Docker image tag                                                                                                                               | `${CHART_VERSION}`            |
| `imagePullSecrets`             | Specify Image pull secrets                                                                                                                     | `[]`                          |
| `podAnnotations`               | Map of annotations to add to the pods                                                                                                          | See `values.yaml`             |
| `podSecurityContext`           | Map of security context to add to the pod                                                                                                      | See `values.yaml`             |
| `securityContext`              | Map of security context to add to the container                                                                                                | See `values.yaml`             |
| `service.type`                 | Service type                                                                                                                                   | `ClusterIP`                   |
| `extraEnv`                     | Map of environment variables to include in the container                                                                                       | `{}`                          |
| `console.dex.host`             | HTTP(S) address of the Dex instance                                                                                                            | `https://dex.example.org`     |
| `console.baseUrl`              | HTTP(S) address of the Console                                                                                                                 | `https://console.example.org` |
| `console.impersonateOpenShift` | Install CRDs to trick the Console into showing some OpenShift-exclusive actions which work on Kubernetes. Note: requires `cluster-admin`       | `false`                       |
| `console.oidc.enabled`         | Enable OIDC authentication                                                                                                                     | `true`                        |
| `console.oidc.issuerUrl`       | Issuer of the OIDC server                                                                                                                      | `https://dex.example.org`     |
| `console.oidc.clientId`        | OIDC client ID                                                                                                                                 | `kubernetes`                  |
| `console.oidc.clientSecret`    | OIDC client secret                                                                                                                             | `hunter2`                     |
| `rbac.enabled`                 | Install RBAC to trick the Console into behaving closer to how OpenShift does. Required `cluster-admin` and `console.impersonateOpenShift=true` | `false`                       |
| `ingress.className`            | IngressClass resource to use.                                                                                                                  |                               |
| `sidecars`                     | Arbitrary sidecars to include as-is                                                                                                            | `[]`                          |

Specify each parameter using the `--set key=value[,key=value]` argument to `helm install`.

### Version `0.2.X`

Version `0.2.0` and above require the `networking.k8s.io/v1` API for Ingress which is available in Kubernetes 1.19 and above.