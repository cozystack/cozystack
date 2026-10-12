<!--
https://github.com/cozystack/cozystack/releases/tag/v1.6.2
-->

# v1.6.2 (2026-08-19)

A patch release with six fixes covering the backup-strategy controller, kube-ovn's webhook certificate, Velero CRD upgrades, CNPG barman-cloud backups, `flux-shard-operator`, and the published OpenAPI definitions, plus a release-pipeline reliability fix.

## Fixes

* **fix(backupstrategy-controller): repair lookup-gated backup objects**: The default backup `Strategy` CRs and the Velero `BackupStorageLocation` are gated on a Helm `lookup` performed while the referenced object is still being created; when that lookup came back empty the objects were skipped permanently, since helm-controller does not re-render a release whose chart and values are unchanged. The gate now resolves the default bucket credentials Secret through the RESTMapper, bounds each check, and tolerates an absent Secret instead of looping, so the default backup objects are created reliably instead of silently vanishing for months ([**@mattia-eleuteri**](https://github.com/mattia-eleuteri) in #3524, backport #3731).

* **fix(kube-ovn): reload kubeovn-webhook serving certificate on cert-manager renewal**: `kube-ovn-webhook` loaded its TLS serving certificate once at startup and never re-read it; once cert-manager renewed the backing Secret and the old certificate expired, the apiserver's calls to the webhook failed verification and, because the `MutatingWebhookConfiguration` uses `failurePolicy: Fail`, every pod creation in tenant namespaces was rejected — including `virt-launcher` pods, blocking VMI startup. The webhook now serves its certificate through a reloading callback that re-reads the key pair when the mounted files change and widens `renewBefore` to 720h, so cert-manager renewals are honored without a pod restart ([**@IvanHunters**](https://github.com/IvanHunters) in #3557, backport #3730).

* **fix(velero): apply CRD updates on upgrade via CreateReplace**: Velero's CRDs stayed frozen at whatever version was first installed, since Helm never touches a chart's `crds/` directory on upgrade; when the Velero image moved to a version that added new backup phases, the apiserver rejected phase transitions against the stale CRDs and backups silently stopped while the HelmRelease stayed green. The Velero package now opts into `upgradeCRDs: CreateReplace`, so CRDs are kept current on upgrade and backups keep working ([**@lexfrei**](https://github.com/lexfrei) in #3727, backport #3728).

* **fix(backups): request S3 checksum only when required for barman-cloud (non-AWS S3 / Ceph RGW)**: CNPG's barman-cloud plugin sidecar defaulted to computing a flexible checksum on every upload, which several S3-compatible backends (Ceph RGW, some MinIO / Cloudflare R2 builds) reject outright, so every backup and WAL-archive upload to those backends failed and `ScheduledBackup`s never stored anything. Every barman-cloud `ObjectStore` Cozystack creates — Keycloak's system DB, the postgres app's backup and recovery stores, and the platform-managed system-bucket store — now sets `AWS_REQUEST_CHECKSUM_CALCULATION=when_required`, a safe default accepted by both AWS S3 and the affected backends ([**@androndo**](https://github.com/androndo) in #3417, backport #3767).

* **fix(flux-shard-operator): repair sharded helm-controller crashloop behind an HTTP proxy**: The cloned `helm-controller-shard<i>` Deployment inherited `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` from the flux-aio all-in-one wiring even though a standalone shard needs no external egress; behind an unreachable proxy the controller's blocking startup HTTPS call never completed, the manager never served `/healthz`, and every HelmRelease sharded to that controller was frozen. The sanitisation now also drops the inherited proxy env and adds a `startupProbe` derived from the liveness handler, so sharded HelmReleases keep reconciling in proxied environments instead of crashlooping forever ([**@IvanHunters**](https://github.com/IvanHunters) in #3546, backport #3818).

* **fix(api): declare OpenAPIModelName for core and sdn types**: The `core` and `sdn` API groups did not declare `OpenAPIModelName` the way the `apps` group already did, so their published OpenAPI definition names were Go import paths while every `$ref` pointing at them escaped each slash — the two spellings never matched, the reference dangled, and `kubectl apply --validate` failed on any resource against a `cozystack-api` built after the underlying Kubernetes 0.35 change. Declaring `OpenAPIModelName` for `core` and `sdn` too makes every published definition name the dotted Kubernetes model name, so client-side validation against the published OpenAPI works again ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #3808, backport #3812).

## Development, Testing, and CI/CD

* **ci(release): complete the candidate-aware promotion pipeline on release-1.6**: `release-1.6` was missing the e2e and packages-verification jobs that `Promote RC` requires on its target base, so `v1.6.1` was promoted with the rc e2e gate bypassed and the next patch release could not even be dispatched. Adds the `rc-e2e` job, the `verify-release-candidate` checks, `hack/verify-promoted-packages.sh`, `hack/validate-changelog.sh` and regression tests pinning the pipeline's contract, so future patch releases off `release-1.6` run the same e2e and package-verification gates as `main` before promoting, and the tag-time changelog is validated and ported from the tag rather than regenerated ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in #3893).

## Documentation

* **[website] docs: import the operator guides that lived in the cozystack repo**: Moves the operator-facing guides that used to live in the `cozystack` repo over to the documentation site, so operators find them alongside the rest of the docs instead of scattered across two repositories ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/website#648).

* **[website] docs(oidc): document private CA and staging trust**: Documents how to configure tenant OIDC to trust a private certificate authority and staging certificates, closing a gap for operators running their own CA or testing with a staging issuer ([**@myasnikovdaniil**](https://github.com/myasnikovdaniil) in cozystack/website#650).

* **[website] feat(community): add a Community page and link it from the main menu**: Adds a Community page linked from the site's main menu, giving visitors a single place to find how to get in touch with and contribute to the Cozystack community ([**@tym83**](https://github.com/tym83) in cozystack/website#637).

* **[website] chore(telemetry): publish July 2026 and explain how the figures are derived**: Publishes the July 2026 telemetry figures and documents how those figures are derived, giving the community visibility into adoption trends and how the numbers are calculated ([**@tym83**](https://github.com/tym83) in cozystack/website#644).

* **[website] feat(blog): new Blockstor banner**: Adds a new banner promoting Blockstor to the blog, improving the visibility of the storage control plane's announcement ([**@tym83**](https://github.com/tym83) in cozystack/website#646).

## Contributors

Thanks to everyone who contributed to this patch release:

* [**@IvanHunters**](https://github.com/IvanHunters)
* [**@androndo**](https://github.com/androndo)
* [**@lexfrei**](https://github.com/lexfrei)
* [**@mattia-eleuteri**](https://github.com/mattia-eleuteri)
* [**@myasnikovdaniil**](https://github.com/myasnikovdaniil)
* [**@tym83**](https://github.com/tym83)

**Full Changelog**: https://github.com/cozystack/cozystack/compare/v1.6.1...v1.6.2
