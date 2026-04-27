# lineage-controller-webhook

Cozystack system package for the **lineage controller webhook** — a mutating
admission webhook that stamps "lineage" labels onto tenant workloads, linking
each resource back to the Cozystack `Application` that ultimately owns it.

The webhook intercepts CREATE and UPDATE on `pods`, `secrets`, `services`,
`persistentvolumeclaims`, `ingresses.networking.k8s.io`, and
`workloadmonitors.cozystack.io` outside system namespaces. For each request it
walks the ownership graph upward (Kubernetes `ownerReferences`, then the
`HelmRelease` Flux installed the resource with, then the Cozystack
`Application`-derived CRD that produced the HelmRelease) and writes the
discovered application's group, kind and name as labels
(`apps.cozystack.io/application.{group,kind,name}`) on the incoming object.
Those labels let the aggregated API server, the Cozystack dashboard, and other
lineage-aware consumers reason about which application a resource belongs to.

The webhook serves TLS on port 9443 with a cert-manager issued certificate, and
runs with `failurePolicy: Fail` — meaning every CREATE/UPDATE on the resources
above is gated on the webhook being reachable. Sizing and topology should
account for that; see "Topology" below.

## Topology

The webhook only needs to be reachable from the kube-apiserver, so the chart
exposes five settings that let a cluster admin pick the topology that fits
their cluster. The settings interact in two non-obvious ways:

- `deployment.replicas` is read only when `deployment.enabled: true`; it is
  silently ignored in DaemonSet mode.
- Deployment mode unconditionally adds `podAntiAffinity` (preferred,
  hostname-topology) so replicas best-effort spread across nodes; the spread
  is *soft* — under scheduler pressure two replicas can still co-locate. There
  is no knob to turn the antiAffinity off; reach for `topologySpreadConstraints`
  on the pod template if you need a hard guarantee.

Everything else can be combined freely. All keys below live under the
top-level `lineageControllerWebhook:` map in your values.

| Knob                            | Default          | Purpose                                                                                                |
| ------------------------------- | ---------------- | ------------------------------------------------------------------------------------------------------ |
| `deployment.enabled`            | `false`          | When `false`, render as a `DaemonSet`. When `true`, render as a `Deployment` (with `podAntiAffinity`). |
| `deployment.replicas`           | `2`              | Replica count when `deployment.enabled: true`. Keep `>= 2` — `failurePolicy: Fail` means a single-replica drain blocks tenant CREATE/UPDATE until the pod reschedules. The chart skips the `PodDisruptionBudget` when `replicas: 1` because a single-replica PDB has no useful value. |
| `nodeAffinity`                  | `[{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists"}]}]` | List of `nodeSelectorTerms` applied as `requiredDuringSchedulingIgnoredDuringExecution`. Set to `[]` to schedule on any node. |
| `tolerations`                   | `[{"operator":"Exists"}]` | Tolerations applied to the pod. Default tolerates every taint so the pod can land on control-plane nodes. |
| `localK8sAPIEndpoint.enabled`   | `true`           | Inject `KUBERNETES_SERVICE_HOST=status.hostIP` and `KUBERNETES_SERVICE_PORT=6443` so the controller talks to the kube-apiserver on its own node. Only valid when the pod is scheduled on a node that hosts an apiserver. The chart will refuse to render when `localK8sAPIEndpoint.enabled: true` is combined with `nodeAffinity: []` — that combination contradicts the local-endpoint contract and almost certainly means the user forgot to disable the endpoint. |

### Example topologies

The default values give you the long-standing behaviour: a DaemonSet pinned to
control-plane nodes, with each pod talking to its local kube-apiserver. The
combinations below are all reachable through values overrides; pick whichever
matches your cluster.

**Default — DaemonSet on control-plane, local API endpoint** (Talos / k3s / kubeadm):

```yaml
# values as shipped — no override needed.
```

One webhook pod per control-plane node, each talking to its own apiserver via
`status.hostIP:6443`. The `nodeAffinity` default uses `operator: Exists` on
`node-role.kubernetes.io/control-plane`, which matches both Talos's empty value
and k3s/kubeadm's `"true"`.

**Deployment on control-plane, local API endpoint** (large control-plane, save
resources):

```yaml
lineageControllerWebhook:
  deployment:
    enabled: true
    replicas: 2
```

Two replicas best-effort spread across control-plane nodes via
`podAntiAffinity` (soft constraint), each still talking to the local apiserver.
Useful when you have many control-plane nodes and don't want one webhook pod
per node.

**Deployment anywhere, no local API endpoint** (managed K8s, Cozy-in-Cozy
tenants where control-plane nodes aren't visible):

```yaml
lineageControllerWebhook:
  deployment:
    enabled: true
    replicas: 2
  nodeAffinity: []
  localK8sAPIEndpoint:
    enabled: false
```

Two replicas scheduled on any node that tolerates the default `Exists`
toleration; the controller falls back to the in-cluster default kube-apiserver
service. If you forget the `localK8sAPIEndpoint.enabled: false` line in this
configuration the chart will refuse to render with a clear error — the
local-endpoint env vars only make sense when the pod is co-located with an
apiserver.

**Deployment on dedicated webhook nodes**:

```yaml
lineageControllerWebhook:
  deployment:
    enabled: true
    replicas: 2
  nodeAffinity:
  - matchExpressions:
    - key: node-role.example.com/admission
      operator: Exists
  tolerations:
  - key: node-role.example.com/admission
    operator: Exists
  localK8sAPIEndpoint:
    enabled: false
```

> ⚠️ **Whenever `nodeAffinity` does not pin the pod to a node that hosts a
> kube-apiserver, set `localK8sAPIEndpoint.enabled: false`.** The chart's
> `fail` guard catches this only for the empty-list case (`nodeAffinity: []`)
> — it cannot inspect arbitrary custom labels to know whether they refer to
> apiserver nodes. With a non-apiserver `nodeAffinity` and the local endpoint
> still enabled, the pods will start but crash-loop dialing
> `status.hostIP:6443` on a node that doesn't run an apiserver. With
> `failurePolicy: Fail` on the webhook, that means tenant CREATE/UPDATE
> outage.

## Service traffic distribution

The `Service` uses `spec.trafficDistribution: PreferClose` (rather than the
older `internalTrafficPolicy: Local`). The kube-apiserver still prefers a
local webhook endpoint when one exists — so DaemonSet-on-control-plane keeps
its locality benefit — but when no local endpoint is available (e.g., a
Deployment with replicas spread across only some nodes, or briefly during a
pod restart) traffic transparently falls over to a remote endpoint instead
of being dropped. With `failurePolicy: Fail` on the MutatingWebhookConfiguration,
that fallback is the difference between a transient hiccup and a tenant
CREATE/UPDATE outage. Requires Kubernetes ≥ 1.31; on older clusters the
field is silently ignored and traffic uses default cluster-wide distribution
(still safe, just no locality preference).

## Parameters

### Image and runtime

| Name      | Description                                                | Value                                                                                       |
| --------- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `image`   | Container image (digest-pinned)                            | `ghcr.io/cozystack/cozystack/lineage-controller-webhook:v1.3.0@sha256:e898…6fb0`            |
| `debug`   | Enable `--zap-log-level=debug` instead of `info`           | `false`                                                                                     |

### Workload

| Name                            | Description                                                                                          | Value                          |
| ------------------------------- | ---------------------------------------------------------------------------------------------------- | ------------------------------ |
| `deployment.enabled`            | DaemonSet (`false`) vs Deployment (`true`)                                                           | `false`                        |
| `deployment.replicas`           | Replica count when running as a Deployment. Keep `>= 2`; PDB is skipped at `1`.                      | `2`                            |
| `nodeAffinity`                  | List of `nodeSelectorTerms`. Set to `[]` to schedule on any node.                                    | `[{"matchExpressions":[{"key":"node-role.kubernetes.io/control-plane","operator":"Exists"}]}]` |
| `tolerations`                   | List of pod tolerations. Default tolerates every taint.                                              | `[{"operator":"Exists"}]`      |
| `localK8sAPIEndpoint.enabled`   | Talk to the kube-apiserver via `status.hostIP:6443`. Only valid when scheduled on an apiserver node. | `true`                         |
