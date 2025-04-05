# keycloak-operator

![Version: 1.25.0](https://img.shields.io/badge/Version-1.25.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: 1.25.0](https://img.shields.io/badge/AppVersion-1.25.0-informational?style=flat-square)

A Helm chart for KubeRocketCI Keycloak Operator

**Homepage:** <https://docs.kuberocketci.io/>

## Overview

Keycloak Operator is a KubeRocketCI operator responsible for configuring existing Keycloak instances. The operator runs both on OpenShift and Kubernetes.

_**NOTE:** Operator is platform-independent, which is why there is a unified instruction for deployment._

## Prerequisites

1. Linux machine or Windows Subsystem for Linux instance with [Helm 3](https://helm.sh/docs/intro/install/) installed;
2. Cluster admin access to the cluster;

## Installation Using Helm Chart

To install the Keycloak Operator, follow the steps below:

1. To add the Helm EPAMEDP Charts for a local client, run "helm repo add":

     ```bash
     helm repo add epamedp https://epam.github.io/edp-helm-charts/stable
     ```

2. Choose the available Helm chart version:

     ```bash
     helm search repo epamedp/keycloak-operator -l
     NAME                           CHART VERSION   APP VERSION     DESCRIPTION
     epamedp/keycloak-operator      1.24.0          1.24.0          A Helm chart for KRCI Keycloak Operator
     ```

    _**NOTE:** It is highly recommended to use the latest stable version._

3. Full chart parameters available below.

4. Install the operator in the <edp-project> namespace with the helm command; find below the installation command example:

    ```bash
    helm install keycloak-operator epamedp/keycloak-operator --version <chart_version> --namespace <edp-project> --set name=keycloak-operator
    ```

5. Check the <edp-project> namespace containing Deployment with your operator in running status.

## Quick Start

1. Create a User in the Keycloak `Master` realm, and assign a `create-realm` role.

2. Insert newly created user credentials into Kubernetes secret:

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name:  keycloak-access
    type: Opaque
    data:
      username: dXNlcg==   # base64-encoded value of "user"
      password: cGFzcw==   # base64-encoded value of "pass"
    ```

3. Create Custom Resource `kind: Keycloak` with Keycloak instance URL and secret created on the previous step:

    ```yaml
    apiVersion: v1.edp.epam.com/v1
    kind: Keycloak
    metadata:
      name: keycloak-sample
    spec:
      secret: keycloak-access             # Secret name
      url: https://keycloak.example.com   # Keycloak URL
    ```

    Wait for the `.status` field with  `status.connected: true`

4. Create Keycloak realm and group using Custom Resources:

   ```yaml
   apiVersion: v1.edp.epam.com/v1
   kind: KeycloakRealm
   metadata:
    name: keycloakrealm-sample
   spec:
    realmName: realm-sample
    keycloakOwner: keycloak-sample   # the name of `kind: Keycloak`
    ```

    ```yaml
    apiVersion: v1.edp.epam.com/v1
    kind: KeycloakRealmGroup
    metadata:
      name: argocd-admins
    spec:
      name: ArgoCDAdmins
      realm: keycloakrealm-sample   # the name of `kind: KeycloakRealm`
    ```

    Inspect [available custom resource](./docs/arch.md) and [CR templates folder](./deploy-templates/_crd_examples/) for more examples

## Local Development

To develop the operator, first set up a local environment, and refer to the [Local Development](https://docs.kuberocketci.io/docs/developer-guide/local-development) page.

Development versions are also available from the [snapshot helm chart repository](https://epam.github.io/edp-helm-charts/snapshot/) page.

### Related Articles

* [Install KubeRocketCI](https://docs.kuberocketci.io/docs/operator-guide/install-kuberocketci)

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| epmd-edp | <SupportEPMD-EDP@epam.com> | <https://solutionshub.epam.com/solution/kuberocketci> |
| sergk |  | <https://github.com/SergK> |

## Source Code

* <https://github.com/epam/edp-keycloak-operator>

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity for pod assignment |
| annotations | object | `{}` | Annotations to be added to the Deployment |
| clusterReconciliationEnabled | bool | `false` | If clusterReconciliationEnabled is true, the operator reconciles all Keycloak instances in the cluster;  otherwise, it only reconciles instances in the same namespace by default, and cluster-scoped resources are ignored. |
| extraVolumeMounts | list | `[]` | Additional volumeMounts to be added to the container |
| extraVolumes | list | `[]` | Additional volumes to be added to the pod |
| image.repository | string | `"epamedp/keycloak-operator"` | KubeRocketCI keycloak-operator Docker image name. The released image can be found on [Dockerhub](https://hub.docker.com/r/epamedp/keycloak-operator) |
| image.tag | string | `nil` | KubeRocketCI keycloak-operator Docker image tag. The released image can be found on [Dockerhub](https://hub.docker.com/r/epamedp/keycloak-operator/tags) |
| imagePullPolicy | string | `"IfNotPresent"` | If defined, a imagePullPolicy applied to the deployment |
| imagePullSecrets | list | `[]` | If defined, imagePullSecrets are applied to deployment |
| name | string | `"keycloak-operator"` | Application name string |
| nodeSelector | object | `{}` | Node labels for pod assignment |
| resources | object | `{"limits":{"memory":"192Mi"},"requests":{"cpu":"50m","memory":"64Mi"}}` | Resource limits and requests for the pod |
| tolerations | list | `[]` | Node tolerations for server scheduling to nodes with taints |
