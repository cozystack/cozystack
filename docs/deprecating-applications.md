# Retiring a catalog application

Removing an application from `packages/apps/` is not a deletion. The platform stops publishing its chart, but the instances people already created keep running, and nothing in the tree tells you what happens to them. This document is the procedure: how to decide what shape the retirement takes, what breaks if you skip the migration, and what the migration has to do.

## Decide the shape first

| Situation | Shape | Precedent |
| --- | --- | --- |
| A successor application exists | Migrate each instance onto it | migrations `28` (mysql to mariadb), `29` (virtual-machine to vm-disk plus vm-instance) |
| No successor, and instances exist in the field | Freeze them on the artifact they already render from | migration `58` (http-cache) |
| No successor, and no instance anywhere | Remove the platform side outright | the second branch of migration `58` |

You cannot know which of the last two applies fleet-wide, so do not choose between them in the tree. The migration decides per cluster, at upgrade time, by looking for instances. A cluster that never used the application keeps nothing; a cluster that still runs one keeps a working application.

Telemetry tells you how many instances exist across reporting clusters, which is the input to the decision to retire at all. It is not a substitute for the per-cluster branch: an application with four instances still has four owners who did not agree to lose them.

## What removal breaks

Deleting the chart and its `PackageSource` breaks every surviving instance, in a way that produces no error at merge time and no error at upgrade time either. The chain:

1. `packages/core/platform/templates/sources.yaml` globs `sources/*.yaml` and emits each file verbatim, with no `helm.sh/resource-policy: keep`, so Helm prunes the `PackageSource` on upgrade.
2. `internal/operator/packagesource_reconciler.go` stamps a controller `ownerReference` on the `ArtifactGenerator` it creates, so garbage collection takes the generator with the `PackageSource`.
3. The generator owns both `ExternalArtifact` objects, so they go too.
4. Those are the `chartRef` targets of `<app>-rd` in `cozy-system` and of every tenant `<app>-<name>` release. Both sit `Ready=False` permanently.
5. `pkg/registry/apps/application/rest.go` mirrors HelmRelease readiness into the instance status, so the instance reads not ready in `kubectl` and unhealthy in the dashboard while its pods keep serving. `packages/system/monitoring-agents/alerts/flux.yaml` fires `HelmReleaseNotReady` at severity major, and `check-readiness` never reports green again.

The `Package` object survives, because `packages/core/platform/templates/_helpers.tpl` stamps `helm.sh/resource-policy: keep` on every one. Do not tell operators to delete it by hand: `internal/operator/package_reconciler.go` sets a controller `ownerReference` from each generated HelmRelease to its `Package`, so deleting it cascades into uninstalling `<app>-rd`, which takes the `ApplicationDefinition` with it. From that moment the kind stops being served, and a surviving instance disappears from the API with its workloads still running and its owner unable to remove them.

The changelog for `v1.0.0-rc.2` records this exact failure after the ferretdb, mysql and virtual-machine renames, and migrations `28`, `29` and `33` were written to clean it up. Treat it as established, not hypothetical.

## Freezing on the cluster's own artifact

An application with no successor can be kept alive without a second repository and without the platform publishing its chart, because the chart a running instance renders from is already resolved on that cluster as an object. Copy that object and pin the copy.

Read the digest off the live `cozystack-packages` `OCIRepository` rather than writing one into the migration. The migration runs as a `pre-upgrade` hook, before the upgrade re-points that object, so the digest it reads is by definition the release the instances are already rendering from. That makes the freeze a no-op by construction, and it asks no question about which release was the last to ship the chart.

Take the digest from `spec.ref.digest` when the installer pinned one, and otherwise from the digest half of `status.artifact.revision`, which source-controller writes as `<tag>@sha256:<manifest digest>`. Never use `status.artifact.digest`: that is the checksum of the downloaded tarball, not of the manifest, and a source pinned to it resolves to nothing. A source with neither must abort the migration rather than publish a source that points nowhere.

Copy the whole `spec` rather than rebuilding it. The url, the pull secret, the interval and any `verify` block come across with it, so an air-gapped or mirrored registry keeps serving exactly what it already served, under the verification the operator configured. A digest written into the script has none of those properties and resolves only where that exact manifest was mirrored.

Write the copy with no `ownerReferences` and no Helm metadata, and give it the `platform.cozystack.io/no-delete` label: it becomes the only chart source those instances have, so a stray delete strands them the same way the prune would.

Then repoint the `PackageSource` at the copy, add `helm.sh/resource-policy: keep`, and null both `meta.helm.sh/release-name` and `app.kubernetes.io/managed-by`. Helm re-reads the keep annotation off the live object during the prune, which is what lets a `pre-upgrade` hook exempt an object from the upgrade that follows it, and nulling the release metadata stops a later release from adopting it.

Do not rename anything. The `ApplicationDefinition` inside the frozen artifact hardcodes its `chartRef`, so a renamed `PackageSource` leaves every release pointing at an artifact nobody produces. Because the names hold, the rd release and every tenant release keep the `chartRef` they already have, and nothing goes not ready.

## The migration

Select instances by both the `chartRef` name and the `apps.cozystack.io/application.kind` label that migration `22` stamped, so a release whose `chartRef` was never rewritten is still found.

On the branch where instances exist, freeze as above and delete nothing. On the branch where none exist, remove the platform side: the `Package` first, and block on it rather than passing `--wait=false`, because the operator reconciles off an informer cache and a reconcile in flight against the pre-delete entry re-creates the release it owns. Then the rd HelmRelease, its `ApplicationDefinition`, and its Helm storage secrets.

Fail closed on every path. Migrations never re-run, so a swallowed error stamps the version past a fleet that is half migrated. No `|| true`.

Number the migration one past the highest that exists **on main**, not one past the highest in your branch, and set `migrations.targetVersion` one past that. Check for other pull requests in flight that add migrations: whichever lands second has to renumber, and `run-migrations.sh` refuses to advance past a number with no file, so a hole fails the upgrade on every cluster below it.

## Tenant RBAC

`cozy:tenant:admin:base` in `packages/system/cozystack-basics/templates/clusterroles.yaml` enumerates the `apps.cozystack.io` resources a tenant owner may write. Remove the retired application's plural from it: the list tracks the catalog, and an entry for an application nobody ships reads as dead config to the next person pruning it.

The clusters that froze still need the grant, so have the migration apply its own `ClusterRole` carrying `rbac.cozystack.io/aggregate-to-tenant-admin`, with the same verbs the base role granted. Apply it in the hook, before the upgrade drops the rule from the chart, so the access never lapses.

Without it the owner of a surviving instance can see it and not remove it. The wildcard over `apps.cozystack.io` lives in `cozy:tenant:base`, which aggregates into `cozy:tenant` and not into `cozy:tenant:admin`; `cozy:tenant:view:base` is read only; and no role in the tenant chain carries any verb on `helm.toolkit.fluxcd.io`, so there is no way around it from inside a tenant.

## What comes out of the tree

The chart under `packages/apps/<app>/`, its ResourceDefinition under `packages/system/<app>-rd/`, the values type under `api/apps/v1alpha1/<type>/` together with its generated deepcopy, the `PackageSource` under `packages/core/platform/sources/`, the bundle line that installs it, the image build line in the root `Makefile`, the dashboard icon mapping in the console's `sidebar-icons.tsx`, and the plural in `cozy:tenant:admin:base`.

Regenerate rather than hand-delete the generated files: the values type is `cozyvalues-gen` output driven by the chart's own `generate` target, and deepcopy is `controller-gen` over the whole `api/apps/v1alpha1` module.

Leave history alone. Published changelogs are a record, and earlier migrations that name the retired kind replay what those clusters actually did.

## Downstream

Two repositories carry per-application code that nothing here checks. `cozystack/website` lists each app in a hardcoded `Makefile` list and keeps one reference page per documentation version, so the `next` page and the list entry go while released versions stay. `cozystack/terraform-provider-cozystack` hand-writes a resource and a data source per kind, plus a per-kind schema guard that imports this repository's `api/apps/v1alpha1` module and breaks when that pin moves past the removal.

## Checklist

- [ ] The decision to retire is recorded, with usage numbers and the maintenance case
- [ ] The shape is chosen: successor migration, or freeze plus conditional removal
- [ ] The migration branches per cluster and deletes nothing on the branch where instances exist
- [ ] The frozen source copies the live `OCIRepository` and pins the digest read at hook time
- [ ] The `PackageSource` is repointed, annotated `keep` and disowned; names are unchanged
- [ ] The tenant grant is dropped from the chart and re-applied by the migration
- [ ] Every failure path stops before the version stamp
- [ ] The migration number is one past main's highest, `targetVersion` one past that, with in-flight PRs checked
- [ ] Generated files are regenerated, not edited
- [ ] The release note says what an upgraded cluster experiences, on both branches
- [ ] Downstream follow-ups are open and linked in the PR
