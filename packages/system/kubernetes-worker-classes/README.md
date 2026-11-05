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


## Rollback of classified Talos pools

Kairos pools must be removed before reverting to a chart that only understands Talos. For a classified Talos pool, the hoster can materialize its effective baseline and overrides into the old inline field layout. Use the exact installed chart and the intended older chart; do not substitute a different release's defaults. Classless pools need no restoration.

```bash
go run ./cmd/workerclass-restore --kubeconfig /path/to/hoster-kubeconfig --context example --namespace tenant-example --release kubernetes-nodes-example-workers --current-chart /path/to/current-chart --old-chart /path/to/previous-chart
```

The default mode prints the restored inline values only after both server-backed Helm renders agree on worker specs and reference identities. It preserves supported pool fields and materializes Talos/source/module settings without copying platform `_cluster` or `_namespace` reference values into the pool. Add `--apply` to write the proven restoration directly to the HelmRelease with hoster authorization; application admission would intentionally restore a selected class on an empty UPDATE instead.

The tool reads map-valued Secret/ConfigMap `valuesFrom` inputs for render context and refuses unsupported `targetPath` references rather than approximating their semantics. Class selection must be inline. Stop concurrent pool and reference/config changes during rollback: the HelmRelease write uses its original resourceVersion/UID and does not refresh a conflict, but the proof is not an atomic transaction across referenced Secrets and classes. On conflict, re-read and re-prove.

The worker-spec check deliberately ignores chart-version labels, Job execution images and the class's MachineDeployment annotation; it does not ignore changed infrastructure, bootstrap reference names, MachineDeployment spec, health/workload specs or import CA data. If the targeted old chart cannot render or changes those worker settings, the tool refuses the write. Restoration removes classification; it does not promise automatic reclassification on a later upgrade, and it is not the later all-pool Talos migration.

## Compatibility limits

The admission update contract depends on the generic Cozystack API concurrency correction tracked in [cozystack/cozystack#5234](https://github.com/cozystack/cozystack/pull/5234). An API that refreshes a stale resourceVersion around an already admitted object can invalidate oldObject-based decisions. That correction remains outside this package; use the separately identified API variant when testing it.

Kairos remains experimental. The unresolved recovery failure has UNKNOWN root cause; moving class ownership and admission into this package is not its diagnosis or fix. Baseline/override validation and successful admission do not prove worker remediation, driver support or release-to-release image reproducibility.
