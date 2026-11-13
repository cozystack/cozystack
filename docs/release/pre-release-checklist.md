## Pre-release verification checklist

For RCs and final releases, run this before merging the release PR. Each item is here because something has shipped without it and broken users.

- [ ] **The published rc passed mandatory full E2E.** Confirm the promote PR says `✅ RC full e2e was verified`; the normal promote PR itself runs no E2E. Add `full-e2e` manually only when a repeat is required, and treat a ⚠️ bypass line as an explicit release-risk decision.
- [ ] **Upgrade from previous patch** (`vX.Y.(Z-1)`) on a real cluster. `helm upgrade` succeeds, all HelmReleases reach Ready, no CRD schema rejections.
- [ ] **Upgrade from previous minor** (`v(X-1).Y.0`) for RC and minor releases. Same assertions.
- [ ] **At least one tenant with `publishing.ingressName != tenant-root`** and a real Let's Encrypt-staging cert issuance.
- [ ] **Apply-mutate-reapply** on the kubernetes umbrella HelmRelease (catches second-reconcile bugs).
- [ ] **For every CRD-source bump** (kubevirt, cert-manager, flux, cilium, kamaji): `helm template | kubectl apply --dry-run=server` against the freshly-installed CRDs.
- [ ] **For every flux / helm-controller / kubevirt-operator / cert-manager bump:** paste the upstream breaking-changes section of the bump's CHANGELOG into the PR body before merge.
- [ ] **After cutting the tag,** watch `pull-requests-release.yaml::Finalize Release`. If it fails on `Draft release for v... not found`, undraft the release manually and file the workflow regression — this has been a known recurring failure since the `Publish draft release` step lost its explicit `github-token` in refactor `66a756b6`.
- [ ] **The promote PR carries `docs/changelogs/vX.Y.Z.md` and it was verified** per [Changelog pre-publish verification](./changelogs.md#changelog-pre-publish-verification). Finalize publishes this file as the release body, so verifying it before merge is verifying the release notes.
- [ ] **The `cozystack/website` docs PR stays parked until publish.** Promotion opens it early; merging it before the GitHub release for `vX.Y.Z` is live 404s the new version's docs and API links. Merge it right after finalize publishes the release (regular-release step 6, patch step 7).
