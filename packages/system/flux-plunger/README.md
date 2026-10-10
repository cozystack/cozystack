# flux-plunger

Recovers a HelmRelease stuck with `has no deployed releases`: it suspends the release, deletes the stale Helm storage Secret, records the processed revision in the `flux-plunger.cozystack.io/last-processed-version` annotation, and unsuspends.

## The uninstall fence

While a release is suspended for recovery it also carries the finalizer `flux-plunger.cozystack.io/uninstall-fence`. helm-controller skips the Helm uninstall of a suspended HelmRelease that is deleted, so without the fence a delete arriving during recovery would leave the release's resources behind. With it, flux-plunger lifts its suspension, helm-controller runs the uninstall, and flux-plunger then removes the fence. A delete held this way for more than ten minutes raises an `UninstallFenceHeld` Warning on the HelmRelease.

When flux-plunger stops it gives up the fences it holds itself, within its termination grace period (`shutdownGracePeriodSeconds`, 90 by default, accepted between 40 and 140): its manager is given 10 seconds to stop, and the drain 25 seconds less than the grace period. The next flux-plunger starts while the old one is still terminating, on a rollout as on an eviction or a node drain, so a single writer is kept by leader election: the old pod holds its Lease through the drain without giving it up, and the Lease outlasts the drain, so a new pod waits for it to run out, a little under two minutes at the default, before it acts. A recovery in progress is aborted: the suspension and the fence go together, and the next flux-plunger starts the recovery again. A release being deleted has flux-plunger's suspension lifted and keeps the fence until helm-controller has run its uninstall, which the drain waits for; one helm-controller has not handled when the grace period runs out keeps its fence. Uninstalling this chart then releases whatever is left: a pre-delete hook stops flux-plunger, waits out its grace period, and lifts any remaining suspension and fence without waiting again. It lifts a suspension only where flux-plunger is the sole manager holding it, which is the outcome flux-plunger's own relinquish has, so a suspension another manager also holds stays; it does so with a patch of `spec.suspend` alone rather than a `kubectl apply`, which on a release last written by a client-side apply would take over and strip the fields that apply set. A release it unfences that was being deleted, that helm-controller's own finalizer no longer held and whose latest generation helm-controller had not observed, is deleted without an uninstall and is named as such. The Job's 240-second deadline sits under the 300 seconds helm-controller allows a pre-delete hook by default. A Deployment that cannot be read or scaled down (flux-plunger would keep running and could fence behind the sweep) ends the hook before any fence is touched, a listing that never succeeds ends it before any fence is touched too, and a fence the hook cannot remove ends it after the sweep; either way the hook exits non-zero, which fails the uninstall and makes helm-controller retry it, hook included. A flux-plunger Deployment that is already gone has nothing to stop, and a flux-plunger pod that is still there after its grace period, typically one stuck `Terminating` on a `NotReady` node, is named and the sweep goes on, since failing on it would fail every retry until the node returns or the pod is force-deleted; because such a pod may still be fencing, the fences are listed again after the sweep, and any found fails the hook. Its report, `released N of M fenced HelmReleases, K without an uninstall` and a line per release, stays readable for ten minutes after the Job finishes, success included: `kubectl logs --namespace cozy-fluxcd job/flux-plunger-uninstall-fence-cleanup`.

If flux-plunger is merely stopped (scaled to zero) rather than uninstalled, or killed without a graceful stop, a fenced release that is deleted and that helm-controller has not uninstalled stays `Terminating` until flux-plunger runs again. To release one by hand, after confirming its uninstall has run or is not wanted, remove the finalizer at the position `INDEX` it has in `metadata.finalizers`; the `test` operation makes the patch fail rather than remove another finalizer if the list has changed:

```bash
kubectl patch helmrelease NAME --namespace NAMESPACE --type json --patch '[{"op":"test","path":"/metadata/finalizers/INDEX","value":"flux-plunger.cozystack.io/uninstall-fence"},{"op":"remove","path":"/metadata/finalizers/INDEX"}]'
```

Fenced releases can be listed with:

```bash
kubectl get helmreleases.helm.toolkit.fluxcd.io --all-namespaces --output go-template='{{range .items}}{{$ns := .metadata.namespace}}{{$name := .metadata.name}}{{range .metadata.finalizers}}{{if eq . "flux-plunger.cozystack.io/uninstall-fence"}}{{$ns}}/{{$name}}{{"\n"}}{{end}}{{end}}{{end}}'
```

## Suspensions left by an earlier flux-plunger

Earlier flux-plunger releases set `spec.suspend` through the shared `flux-client-side-apply` manager, so a suspension one of them left behind cannot be told apart from a migration's or an operator's and is treated as external: it is not cleared automatically. Such a leftover exists only if the old pod stopped in the middle of a recovery, and flux-plunger raises a `SuspensionNotOwned` Warning on it. To list candidates, suspended releases carrying the flux-plunger annotation whose `spec.suspend` is not owned by flux-plunger's apply:

```bash
kubectl get helmreleases.helm.toolkit.fluxcd.io --all-namespaces --output json | jq --raw-output '.items[] | select(.spec.suspend == true) | select(.metadata.annotations["flux-plunger.cozystack.io/last-processed-version"] != null) | select([.metadata.managedFields[]? | select(.manager == "flux-plunger" and .operation == "Apply" and .fieldsV1["f:spec"]["f:suspend"] != null)] | length == 0) | "\(.metadata.namespace)/\(.metadata.name)"'
```

Clearing one is an operator action: confirm nothing else (a migration, a restore) is holding it, then resume it with `flux resume helmrelease NAME --namespace NAMESPACE`.
