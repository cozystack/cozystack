# flux-plunger

Recovers a HelmRelease stuck with `has no deployed releases`: it suspends the release, deletes the stale Helm storage Secret, records the processed revision in the `flux-plunger.cozystack.io/last-processed-version` annotation, and unsuspends.

## The uninstall fence

While a release is suspended for recovery it also carries the finalizer `flux-plunger.cozystack.io/uninstall-fence`. helm-controller skips the Helm uninstall of a suspended HelmRelease that is deleted, so without the fence a delete arriving during recovery would leave the release's resources behind. With it, flux-plunger lifts its suspension, helm-controller runs the uninstall, and flux-plunger then removes the fence. A delete held this way for more than ten minutes raises an `UninstallFenceHeld` Warning on the HelmRelease.

Uninstalling this chart releases every fence first: a pre-delete hook stops flux-plunger, lifts the suspensions it holds, gives helm-controller a bounded chance to uninstall any release already being deleted, and removes the fence.

If flux-plunger is merely stopped (scaled to zero) rather than uninstalled, a fenced release that is deleted stays `Terminating` until flux-plunger runs again. To release one by hand, after confirming its uninstall has run or is not wanted, remove the finalizer at the position `INDEX` it has in `metadata.finalizers`; the `test` operation makes the patch fail rather than remove another finalizer if the list has changed:

```bash
kubectl patch helmrelease NAME --namespace NAMESPACE --type json --patch '[{"op":"test","path":"/metadata/finalizers/INDEX","value":"flux-plunger.cozystack.io/uninstall-fence"},{"op":"remove","path":"/metadata/finalizers/INDEX"}]'
```

Fenced releases can be listed with:

```bash
kubectl get helmreleases.helm.toolkit.fluxcd.io --all-namespaces --output go-template='{{range .items}}{{$ns := .metadata.namespace}}{{$name := .metadata.name}}{{range .metadata.finalizers}}{{if eq . "flux-plunger.cozystack.io/uninstall-fence"}}{{$ns}}/{{$name}}{{"\n"}}{{end}}{{end}}{{end}}'
```
