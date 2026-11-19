# cozyplane-apiserver Helm chart

Serves the `sdn.cozystack.io` API group — the tenant kinds `VPC`, `VPCBinding`,
`VPCPeering`, `VPCGateway`, `NATGateway`, `RouteTable`, `Port`, `SecurityGroup`,
`HostFirewall`, `ServiceVIP` and `FloatingIP` — from an aggregated API server
backed by its own etcd. No chart installs CRDs for this group; this chart is the
only thing that serves it. The [cozyplane](../cozyplane) CNI chart installs only
the `local.sdn.cozystack.io` CRDs (see
[docs/api-groups.md](../../docs/api-groups.md)).

The chart installs:

- the `cozyplane-apiserver` Deployment and Service;
- its etcd: an operator-managed `EtcdCluster` (default) or a built-in single-pod
  etcd ([Storage](#storage));
- cert-manager `Issuer`s and `Certificate`s for the serving certificate and the
  etcd PKI;
- RBAC: delegated authentication and authorization, and the server's own
  APIService registration.

## Requirements

- Kubernetes >= 1.30.
- The [cozyplane](../cozyplane) chart, installed and its nodes Ready. The
  apiserver and etcd run as default-network pods.
- cert-manager.
- For the default etcd: the aenix-io etcd-operator
  (`etcd-operator.cozystack.io/v1alpha2`) and a StorageClass named `replicated`,
  or another class set in `etcd.operator.storageClass`.
- For the built-in etcd: a default StorageClass, unless the etcd runs on
  emptyDir.

## Install order

1. **cozyplane** (the CNI). It has no cert-manager dependency, so it installs
   first, on a cluster with no other CNI.
2. **cert-manager**, the **etcd-operator** and the **storage layer**. These run on
   the default network the CNI provides.
3. **cozyplane-apiserver** (this chart).

```sh
helm install cozyplane ./chart/cozyplane -n cozy-cozyplane --create-namespace
# install cert-manager, the etcd-operator and a StorageClass
helm install cozyplane-apiserver ./chart/cozyplane-apiserver -n cozy-cozyplane
```

In Cozystack the two charts are separate PackageSources:
`packages/packagesources/networking.yaml` (the CNI, no `dependsOn`) and
`packages/packagesources/apiserver.yaml` (`dependsOn` cert-manager, the
etcd-operator and the storage layer).

## How the group becomes available

The chart ships no `APIService`. The server creates `v1alpha1.sdn.cozystack.io`
at startup, pointing at Service `cozyplane-apiserver`, with the cert-manager
cainjector annotation that fills its CA bundle. It re-creates or re-patches the
object every 30 seconds while it serves.

Until that APIService is available, `sdn.cozystack.io` does not exist: creating
a `VPC` fails with `no matches for kind "VPC"`. The default network works
throughout. The cozyplane controller and agents probe discovery every 15
seconds and start their tenant controllers and informers once the group
answers, without a restart.

A fresh install has nothing to migrate: no CRD store for the group exists to
take over from.

## Storage

The aggregated API's objects live only in this chart's etcd. Losing that etcd
loses every `sdn.cozystack.io` object in the cluster.

### Operator-managed etcd (default)

`etcd.operator.enabled: true` creates an `EtcdCluster` named `cozyplane-etcd`.
The apiserver connects to it over mutual TLS; the etcd-operator issues the
member certificates from the chart's cert-manager issuers.

| Value | Default | Meaning |
|-------|---------|---------|
| `etcd.operator.replicas` | `3` | etcd members |
| `etcd.operator.persistent` | `true` | `true`: one PVC per member. `false`: tmpfs, lost when the members restart |
| `etcd.operator.storageClass` | `replicated` | StorageClass of the member PVCs; `""` is the cluster default |
| `etcd.operator.size` | `4Gi` | PVC size per member, or the tmpfs cap |
| `etcd.operator.version` | `3.6.11` | etcd version the operator runs |

The storage medium and StorageClass cannot change after the `EtcdCluster` is
created. Set them before the first install.

### Built-in single-pod etcd

`etcd.operator.enabled: false` runs one etcd pod instead: a Deployment and
Service named `cozyplane-etcd`, plaintext on port 2379, with no replication. Use
it where the etcd-operator is absent, such as kind.

By default its data is on a PVC the chart creates:

| Value | Default | Meaning |
|-------|---------|---------|
| `etcd.persistence.enabled` | `true` | create the PVC `cozyplane-etcd-data` (ReadWriteOnce) |
| `etcd.persistence.storageClassName` | `""` | StorageClass of the PVC; `""` is the cluster default |
| `etcd.persistence.size` | `2Gi` | PVC size |
| `etcd.storage` | the PVC above | the etcd data volume source |

On a cluster with no StorageClass, put the data on an emptyDir. Every
`sdn.cozystack.io` object is then lost when the pod is deleted or rescheduled.

```yaml
etcd:
  operator:
    enabled: false
  persistence:
    enabled: false
  storage:
    persistentVolumeClaim: null
    emptyDir: {}
```

## Legacy: installs that served `sdn.cozystack.io` from CRDs

This section applies only to clusters installed from cozyplane before
2026-07-12, when the cozyplane chart shipped the tenant kinds as CRDs and this
chart took the group over from them. No install from the current charts has
such CRDs.

No supported migration path exists for these clusters. Two facts matter before
upgrading one:

- The old cozyplane chart rendered the tenant CRDs as templates without
  `helm.sh/resource-policy: keep`. Upgrading that release to the current chart
  deletes the CRDs and every object stored in them. Export the objects first.
- Exported objects do not re-apply unchanged: `Port.spec.fabricIP` was removed
  on 2026-07-13 and the `ExternalPool` kind on 2026-07-18.

A tenant CRD that survives the upgrade breaks the group's OpenAPI, and with it
`kubectl apply` of every object in the group. The server's
`--remove-bootstrap-crds` flag deletes those CRDs at startup. The chart grants
the RBAC for it but does not set the flag.
