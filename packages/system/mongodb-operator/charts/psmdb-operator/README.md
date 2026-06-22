# Percona Operator for MongoDB

Percona Operator for MongoDB allows users to deploy and manage Percona Server for MongoDB Clusters on Kubernetes.
Useful links:
- [Operator Github repository](https://github.com/percona/percona-server-mongodb-operator)
- [Operator Documentation](https://www.percona.com/doc/kubernetes-operator-for-psmongodb/index.html)

## Pre-requisites
* Kubernetes 1.32+
* Helm v3

# Installation

This chart will deploy the Operator Pod for the further Percona Server for MongoDB creation in Kubernetes.

## Installing the chart

There are two ways to insall operator: using separate chart with operator Custom Resource Defenitions or using CRDs from the `crds/` directory.

### Using separate chart

Install CRD chart:

```sh
helm repo add percona https://percona.github.io/percona-helm-charts/
helm install psmdb-operator-crds percona/psmdb-operator-crds --namespace psmdb --create-namespace
```
> **Note:** Deleting CRD chart will trigger deletion of all the custom resources created using the CRDs thus deleting all clusters.
>  Uninstalling percona/psmdb-operator-crds chart should be approached with caution.


Install operator chart:
```sh
helm install my-operator percona/psmdb-operator --version 1.22.0 --namespace my-namespace
```

### Using the `crds/` directory.

```sh
helm repo add percona https://percona.github.io/percona-helm-charts/
helm install my-operator percona/psmdb-operator --version 1.22.0 --namespace my-namespace
```

## Upgrading CRDs

By default, Helm installs CRDs from the `crds/` directory only during initial installation and does not upgrade them. To upgrade CRDs, you have two options:

### Option 1: Use the Separate CRD Chart

Install or upgrade CRDs using the dedicated CRD chart:

```sh
helm repo update
helm upgrade --install psmdb-operator-crds percona/psmdb-operator-crds --namespace my-namespace
```

> **Note:** If you're using Helm 3.17.0 or later, use `--take-ownership` to take over CRDs that were previously installed via the `crds/` directory:
>
> ```sh
> helm upgrade --install psmdb-operator-crds percona/psmdb-operator-crds --namespace my-namespace --take-ownership
> ```

For Helm versions older than 3.17.0, manually add ownership labels and annotations before running the upgrade:

```sh
CRDS=(perconaservermongodbs.psmdb.percona.com perconaservermongodbbackups.psmdb.percona.com perconaservermongodbrestores.psmdb.percona.com)
kubectl label crds "${CRDS[@]}" app.kubernetes.io/managed-by=Helm --overwrite
kubectl annotate crds "${CRDS[@]}" meta.helm.sh/release-name=psmdb-operator-crds --overwrite
kubectl annotate crds "${CRDS[@]}" meta.helm.sh/release-namespace=my-namespace --overwrite
```

### Option 2: Use kubectl to upgrade the CRDs

kubectl apply --server-side -f https://raw.githubusercontent.com/percona/percona-server-mongodb-operator/v1.22.0/deploy/crd.yaml -n my-namespace


The chart can be customized using the following configurable parameters:

| Parameter                    | Description                                                                                                  | Default                                   |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `image.repository`           | PSMDB Operator Container image name                                                                          | `percona/percona-server-mongodb-operator` |
| `image.tag`                  | PSMDB Operator Container image tag                                                                           | `1.22.0`                                  |
| `image.pullPolicy`           | PSMDB Operator Container pull policy                                                                         | `IfNotPresent`                            |
| `imagePullSecrets`           | PSMDB Operator Pod pull secret                                                                               | `[]`                                      |
| `replicaCount`               | PSMDB Operator Pod quantity                                                                                  | `1`                                       |
| `revisionHistoryLimit`       | Quantity of old ReplicaSets to retain for rollback purposes                                                  | ``                                        |
| `tolerations`                | List of node taints to tolerate                                                                              | `[]`                                      |
| `annotations`                | PSMDB Operator Deployment annotations                                                                        | `{}`                                      |
| `podAnnotations`             | PSMDB Operator Pod annotations                                                                               | `{}`                                      |
| `labels`                     | PSMDB Operator Deployment labels                                                                             | `{}`                                      |
| `podLabels`                  | PSMDB Operator Pod labels                                                                                    | `{}`                                      |
| `resources`                  | Resource requests and limits                                                                                 | `{}`                                      |
| `nodeSelector`               | Labels for Pod assignment                                                                                    | `{}`                                      |
| `podSecurityContext`         | Pod Security Context                                                                                         | `{}`                                      |
| `watchNamespace`             | Set when a different from default namespace is needed to watch (comma separated if multiple needed)          | `""`                                      |
| `createNamespace`            | Set if you want to create watched namespaces with helm                                                       | `false`                                   |
| `rbac.create`                | If false RBAC will not be created. RBAC resources will need to be created manually                           | `true`                                    |
| `securityContext`            | Container Security Context                                                                                   | `{}`                                      |
| `serviceAccount.create`      | If false the ServiceAccounts will not be created. The ServiceAccounts must be created manually               | `true`                                    |
| `serviceAccount.annotations` | PSMDB Operator ServiceAccount annotations                                                                    | `{}`                                      |
| `logStructured`              | Force PSMDB operator to print JSON-wrapped log messages                                                      | `false`                                   |
| `logLevel`                   | PSMDB Operator logging level                                                                                 | `INFO`                                    |
| `disableTelemetry`           | Disable sending PSMDB Operator telemetry data to Percona                                                     | `false`                                   |
| `maxConcurrentReconciles`    | Number of concurrent workers that can reconcile resources in Percona Server for MongoDB clusters in parallel | `1`                                       |

Specify parameters using `--set key=value[,key=value]` argument to `helm install`

Alternatively a YAML file that specifies the values for the parameters can be provided like this:

```sh
helm install psmdb-operator -f values.yaml percona/psmdb-operator
```

## Deploy the database

To deploy Percona Server for MongoDB run the following command:

```sh
helm install my-db percona/psmdb-db
```

See more about Percona Server for MongoDB deployment in its chart [here](https://github.com/percona/percona-helm-charts/tree/main/charts/psmdb-db) or in the [Helm chart installation guide](https://www.percona.com/doc/kubernetes-operator-for-psmongodb/helm.html).

# Need help?

**Commercial Support**  | **Community Support** |
:-: | :-: |
| <br/>Enterprise-grade assistance for your mission-critical database deployments in containers and Kubernetes. Get expert guidance for complex tasks like multi-cloud replication, database migration and building platforms.<br/><br/>  | <br/>Connect with our engineers and fellow users for general questions, troubleshooting, and sharing feedback and ideas.<br/><br/>  | 
| **[Get Percona Support](https://hubs.ly/Q02ZTH8Q0)** | **[Visit our Forum](https://forums.percona.com/)** |
