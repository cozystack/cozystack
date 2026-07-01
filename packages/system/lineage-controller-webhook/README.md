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
runs with `failurePolicy: Fail` — every CREATE/UPDATE on the resources above
is gated on the webhook being reachable.

## Topology

The chart ships a single shape, modelled on `cozystack-api`:

- **Deployment** with two replicas (override via `replicas`).
- **Soft `nodeAffinity`** preferring `node-role.kubernetes.io/control-plane`
  (`Exists`, so it matches both Talos's empty value and k3s/kubeadm's `"true"`).
  Soft means: the pod lands on a control-plane node when one is reachable, and
  on any worker otherwise — no override needed for managed Kubernetes,
  Cozy-in-Cozy tenants, or any other cluster where control-plane nodes aren't
  visible.
- **Permissive `tolerations`** (`[{operator: Exists}]`) so the pod can land on
  tainted control-plane nodes when the soft affinity is satisfiable.
- **Soft `podAntiAffinity`** on `kubernetes.io/hostname` so replicas spread
  across nodes when possible (best-effort).
- **`PodDisruptionBudget`** with `maxUnavailable: 1`, unconditional. At
  `replicas: 1` it's a useful no-op (allows full drain); at `replicas >= 2`
  it caps disruption to one pod.
- **`Service` with `spec.trafficDistribution: PreferClose`** so the apiserver
  prefers a webhook endpoint on its own node when one exists, and transparently
  falls over to a remote endpoint otherwise. Requires Kubernetes ≥ 1.31; on
  older clusters the field is silently ignored and traffic uses default
  cluster-wide distribution.

## Parameters

All keys live under the top-level `lineageControllerWebhook:` map.

| Name                          | Description                                               | Value                                                                            |
| ----------------------------- | --------------------------------------------------------- | -------------------------------------------------------------------------------- |
| `image`                       | Container image (digest-pinned)                           | `ghcr.io/cozystack/cozystack/lineage-controller-webhook:v1.3.0@sha256:e898…6fb0` |
| `debug`                       | Enable `--zap-log-level=debug` instead of `info`          | `false`                                                                          |
| `replicas`                    | Deployment replica count                                  | `2`                                                                              |
| `localK8sAPIEndpoint.enabled` | **Deprecated.** See note below.                           | `false`                                                                          |

### `localK8sAPIEndpoint.enabled` (deprecated)

When enabled, this injects `KUBERNETES_SERVICE_HOST=status.hostIP` and
`KUBERNETES_SERVICE_PORT=6443` so the webhook talks to the kube-apiserver on
its own node. It was originally added to avoid latency on the
webhook-to-apiserver path, but it is only valid when the pod is actually
scheduled on an apiserver-bearing node — which the chart's soft control-plane
affinity no longer guarantees. With this flag enabled and the pod scheduled
off a control-plane node, the controller will crash-loop dialing a non-
apiserver hostIP. Slated for removal once the latency motivation is addressed
in the webhook itself; **leave disabled**.
