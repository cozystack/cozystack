## Release-time fires — what's gone wrong recently

These are real regressions that escaped to users. The pattern in each: code passed CI, shipped in a stable release, broke customers. Read them as a working list of pre-release verification gaps.

### v1.3.0 — cert-manager ingressClassName regression (fix #2562)

**Bug:** A commit migrated ACME HTTP-01 to use the modern `acme.cert-manager.io/http01-ingress-ingressclassname` annotation. The matching ingress-shim code that reads that annotation was added by cert-manager upstream PR #8244 and was **never backported** to the `release-1.19` branch — but cozystack 1.3.0 shipped cert-manager v1.19.3. The annotation was silently dropped at runtime and every solver Ingress fell back to the ClusterIssuer's default class `tenant-root`. Let's Encrypt validation broke for every tenant whose `publishing.ingressName != tenant-root`.

**Blast radius:** v1.3.0, v1.3.1, and v1.1.7 (the migration was cherry-picked to `release-1.1`).

**Why CI missed it:** the migration commit was self-contained and `helm template` rendered cleanly. There was no E2E that creates a tenant with a non-default ingress class and watches a real Let's Encrypt cert issue. Verification had been done from the docs side ("the API has been available since cert-manager 1.12"), not against the shipped cert-manager source.

**Would have caught it:** non-root-tenant E2E with LE-staging cert issuance, asserting the solver Ingress lands on the tenant's class.

### v1.4.0 — kubevirt-instancetypes upgrade failure (fix #2612)

**Bug:** `packages/system/kubevirt-instancetypes/Makefile` contained `sed -i '/persistent: true/d' templates/preferences.yaml`, which stripped the only child of `preferredTPM`, leaving `preferredTPM:` (null) in six Windows preferences. KubeVirt v1.6.x silently accepted it. The v1.6.3 → v1.8.2 operator bump (2026-04-27) hardened the CRD's OpenAPI schema and started rejecting null for an object-typed field. `helm upgrade` then failed on every cluster with `kubevirt-instancetypes` enabled.

**Why CI missed it:** the kubevirt-operator bump and the inert `preferences.yaml` were two weeks apart in commit history. CI only tests fresh installs against the new CRDs; it doesn't run `helm upgrade` from prior versions. Schema regression on a vendored asset that hadn't been touched in 18 months.

**Would have caught it:** `helm template <new chart> | kubectl apply --dry-run=server -f -` against the freshly-installed CRDs of every bumped operator. Or an upgrade-from-N-1 E2E lane.

### v1.4.0 — Flux 2.7 → 2.8 readiness deadlock (#2602)

**Bug:** helm-controller v1.5.0 (shipped in Flux 2.8) changes the default wait strategy to kstatus polling, which polls **every applied resource including child HelmReleases**. The `packages/apps/kubernetes` umbrella has 19 child HRs that each `dependsOn: parent` (with a `lookup` guard so the dependency only activates after the parent exists). Pre-2.8: parent went Ready as soon as Helm returned. Post-2.8: parent waits for children, children wait for parent. Deadlock.

**Why CI missed it:** the cycle only manifests on the second reconcile (parent exists, so the `lookup` activates the child's `dependsOn`). Fresh-install E2E dodges this — the parent-already-exists branch isn't taken on the first apply.

**Would have caught it:** apply-mutate-reapply E2E (any chart bump in the kubernetes umbrella triggers a second reconcile). Plus an explicit PR-template requirement: for any flux/helm-controller bump, paste the upstream breaking-changes section into the PR body.

### Patterns these share

1. **Fresh-install CI is insufficient.** All three regressions passed fresh-install lanes.
2. **Upstream breaking-defaults bumps are the common shape.** kubevirt CRD hardening, helm-controller default wait strategy, cert-manager API surface drift. Pre-merge reading of upstream changelogs would have caught two of three.
3. **One-engineer-bitten-once is the only safety net for live verification.** The cert-manager bump PR included an explicit live-cluster verification step — because the same engineer had been bitten the prior week. There is no checklist requiring it.
