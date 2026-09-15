# flux-plunger

Recovers a HelmRelease stuck with `has no deployed releases`: it suspends the release, deletes the stale Helm storage Secret, records the processed revision in the `flux-plunger.cozystack.io/last-processed-version` annotation, and unsuspends.

## The uninstall fence

While a release is suspended for recovery it also carries the finalizer `flux-plunger.cozystack.io/uninstall-fence`. helm-controller skips the Helm uninstall of a suspended HelmRelease that is deleted, so without the fence a delete arriving during recovery would leave the release's resources behind. With it, flux-plunger lifts its suspension, helm-controller runs the uninstall, and flux-plunger then removes the fence. A delete held this way for more than ten minutes raises an `UninstallFenceHeld` Warning on the HelmRelease.

Uninstalling this chart releases every fence first: a pre-delete hook stops flux-plunger, lifts the suspensions it holds, gives helm-controller a bounded chance to uninstall any release already being deleted, and removes the fence. The waiting is one shared budget of 150 seconds across all such releases, so it cannot grow with their number; the few API calls each fenced release still needs after that share the rest of the Job's 240-second deadline, which sits under the 300 seconds helm-controller allows a pre-delete hook by default. A release helm-controller has not handled when the budget runs out still loses its fence, and is then deleted without an uninstall: removing the component must not hang on a helm-controller that does not answer. A fence the hook cannot remove, or a listing that never succeeds, is different: the hook retries within the budget, then names each release it left and exits non-zero, which fails the uninstall and makes helm-controller retry it, hook included. Its report, `released N of M fenced HelmReleases` and a `NOT released` line per release left, stays readable for ten minutes after the Job finishes, success included: `kubectl logs --namespace cozy-fluxcd job/flux-plunger-uninstall-fence-cleanup`.

If flux-plunger is merely stopped (scaled to zero) rather than uninstalled, a fenced release that is deleted stays `Terminating` until flux-plunger runs again. To release one by hand, after confirming its uninstall has run or is not wanted, remove the finalizer at the position `INDEX` it has in `metadata.finalizers`; the `test` operation makes the patch fail rather than remove another finalizer if the list has changed:

```bash
kubectl patch helmrelease NAME --namespace NAMESPACE --type json --patch '[{"op":"test","path":"/metadata/finalizers/INDEX","value":"flux-plunger.cozystack.io/uninstall-fence"},{"op":"remove","path":"/metadata/finalizers/INDEX"}]'
```

Fenced releases can be listed with:

```bash
kubectl get helmreleases.helm.toolkit.fluxcd.io --all-namespaces --output go-template='{{range .items}}{{$ns := .metadata.namespace}}{{$name := .metadata.name}}{{range .metadata.finalizers}}{{if eq . "flux-plunger.cozystack.io/uninstall-fence"}}{{$ns}}/{{$name}}{{"\n"}}{{end}}{{end}}{{end}}'
```
