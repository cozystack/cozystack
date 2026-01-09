# Percona Operator for MongoDB

Percona Operator for MongoDB allows users to deploy and manage Percona Server for MongoDB Clusters on Kubernetes.
Useful links:
- [Operator Github repository](https://github.com/percona/percona-server-mongodb-operator)
- [Operator Documentation](https://www.percona.com/doc/kubernetes-operator-for-psmongodb/index.html)

## Pre-requisites
* Kubernetes 1.30+
* Helm v3

# Installation

This chart will deploy the Operator Pod for the further Percona Server for MongoDB creation in Kubernetes.

## Installing the chart

To install the chart with the `psmdb` release name using a dedicated namespace (recommended):

```sh
helm repo add percona https://percona.github.io/percona-helm-charts/
helm install my-operator percona/psmdb-operator --version 1.21.2 --namespace my-namespace
```

The chart can be customized using the following configurable parameters:

| Parameter                    | Description                                                                                                  | Default                                   |
| ---------------------------- | ------------------------------------------------------------------------------------------------------------ | ----------------------------------------- |
| `image.repository`           | PSMDB Operator Container image name                                                                          | `percona/percona-server-mongodb-operator` |
| `image.tag`                  | PSMDB Operator Container image tag                                                                           | `1.21.1`                                  |
| `image.pullPolicy`           | PSMDB Operator Container pull policy                                                                         | `Always`                                  |
| `image.pullSecrets`          | PSMDB Operator Pod pull secret                                                                               | `[]`                                      |
| `replicaCount`               | PSMDB Operator Pod quantity                                                                                  | `1`                                       |
| `tolerations`                | List of node taints to tolerate                                                                              | `[]`                                      |
| `annotations`                | PSMDB Operator Deployment annotations                                                                        | `{}`                                      |
| `podAnnotations`             | PSMDB Operator Pod annotations                                                                               | `{}`                                      |
| `labels`                     | PSMDB Operator Deployment labels                                                                             | `{}`                                      |
| `podLabels`                  | PSMDB Operator Pod labels                                                                                    | `{}`                                      |
| `resources`                  | Resource requests and limits                                                                                 | `{}`                                      |
| `nodeSelector`               | Labels for Pod assignment                                                                                    | `{}`                                      |
| `podAnnotations`             | Annotations for pod                                                                                          | `{}`                                      |
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
