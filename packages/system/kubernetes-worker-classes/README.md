# Worker classes for managed Kubernetes

This package owns `cozystack.io/v1alpha1 WorkerClass`, reference reconciliation and worker-pool admission. Its CRD component installs first and retains the CRD on uninstall. Built-in `talos` and `kairos` objects are informational; custom classes are immutable and held by a finalizer while pools reference them.

The platform forwards `kubernetesWorkerClasses.defaultWorkerClass` to this package's `defaultWorkerClass`. CREATE persists that selection; an UPDATE that omits or empties the field preserves a selected class or leaves a legacy pool legacy. The package matches KubernetesNodes CREATE and UPDATE on its served `kubernetesnodeses.apps.cozystack.io` resource in every namespace, with Exact matching, empty selectors and no matchConditions. `failurePolicy: Fail` blocks those requests if admission is unavailable, including legacy UPDATE. Existing classless Helm renders do not depend on the webhook's availability.

Certificates are issued and rotated by cert-manager. Readiness waits for the TLS listener. The pre-delete hook unregisters both admission configurations before removing their backend; a cleanup permission failure stops normal Helm uninstall rather than leaving an orphaned global Fail webhook. Force deletion or disabling Helm hooks bypasses that ordering and is not supported cleanup. Remove classified pools or restore their effective legacy Talos values before removing this package; the retained CRD does not run its finalizer controller for you.

Direct HelmRelease producers store `workerClassName` inline in `spec.values` when they want class semantics; an omitted field intentionally keeps the legacy path. The webhook does not intercept those raw writes. ComputePlane retains its classless producer contract.

## Parameters

### Parameters

| Name                 | Description                                                                                                                           | Type     | Value                                                                                 |
| -------------------- | ------------------------------------------------------------------------------------------------------------------------------------- | -------- | ------------------------------------------------------------------------------------- |
| `defaultWorkerClass` | Platform-selected class assigned to new standalone worker pools. Changing it does not classify legacy pools or move an existing pool. | `string` | `talos`                                                                               |
| `replicas`           | Admission replicas. Controllers use leader election.                                                                                  | `int`    | `2`                                                                                   |
| `resources`          | Controller and webhook resource requests and limits.                                                                                  | `object` | `{"limits":{"cpu":"500m","memory":"256Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` |

