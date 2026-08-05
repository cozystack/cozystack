# Release Workflow

This document is both the process design (what the release model looks like) and the operational playbook (what to actually do, what breaks, what to verify). Companion to [`agents/changelog.md`](./agents/changelog.md), which is the canonical changelog process.

## When to use this guide

You are about to:

- Cut a pre-release (`vX.Y.0-rc.N`, or `vX.Y.0-alpha.N` / `-beta.N`).
- Cut a regular release (`vX.Y.0`).
- Cut a patch release (`vX.Y.Z` with `Z > 0`).
- Triage cherry-picks before a patch release.
- Investigate why CI is misbehaving on a tag push.
- Review a promote (release) PR.

If you are about to generate a changelog only, jump straight to [`agents/changelog.md`](./agents/changelog.md).

## Introduction

Cozystack uses a staged release process to ensure stability and flexibility during development.

There are three types of releases:

- **Release Candidates (RC)** – Preview versions (e.g., `v1.2.0-rc.1`) used for final testing and validation.
- **Regular Releases** – Final versions (e.g., `v1.2.0`) that are feature-complete and thoroughly tested.
- **Patch Releases** – Bugfix-only updates (e.g., `v1.2.1`) made after a stable release, based on a dedicated release branch.

All three are matched by `tags.yaml`'s regex `^v\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$`. The tag push is the trigger for the whole pipeline. **Pre-release tags (`-alpha` / `-beta` / `-rc`) are cut with the [`Cut Pre-release Tag`](../.github/workflows/cut-prerelease.yaml) workflow** (manual dispatch from `main` or `release-X.Y`) — it pushes the tag as the CI app at the dispatch branch's tip, so GitHub fills `base_ref` (`tags.yaml`'s `Get base branch` step refuses tags pushed without a base) and the push triggers `tags.yaml` (a tag pushed with the default `GITHUB_TOKEN` would not). Stable `vX.Y.Z` tags are never pushed by hand at all — they are cut write-once by the promote flow at a PR merge commit. Only `-rc.N` pre-releases feed the promotion flow ([`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) accepts `vX.Y.Z-rc.N` alone); `-alpha.N` / `-beta.N` tags are built and published for download and validated on demand with the [`e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml) button, but are never promoted to a stable release.

## Release Candidates

Release candidates are Cozystack versions that introduce new features and are published before a stable release. Their purpose is to help validate stability before finalizing a new feature release.

Release candidates are given numbers `vX.Y.0-rc.N`, for example, `v1.2.0-rc.1`. The first one is cut from `main`, once all major features for the upcoming release have merged and the release enters its testing phase.

**Cutting the first rc freezes the line.** [`cut-prerelease.yaml`](../.github/workflows/cut-prerelease.yaml) creates `release-X.Y` at the tagged commit, and from that point the release's content is closed:

- Every later cut for that line — `rc.2`, `rc.3`, an `alpha`/`beta`, or a patch-line rc — must be dispatched from `release-X.Y`. A dispatch from `main` is refused.
- Fixes reach the release only by cherry-pick or backport onto `release-X.Y` (see [Backports](#backports)).
- `main` reopens immediately for the next minor, so feature work never has to wait for the release to ship.

This is what makes an rc mean something. Previously `release-X.Y` was created only when the stable release merged, so `rc.2` was cut from `main`'s tip and silently absorbed everything that had landed since `rc.1` — the shipped release could contain code that no rc had ever validated.

**Un-freezing is manual and deliberate.** If an `rc.1` turns out to be unusable and the line has to be re-cut from `main`, delete `release-X.Y` by hand first; the refuse step blocks every other route back to a `main` dispatch, which is the point of it. Nothing automates the deletion, and nothing should — throwing away a frozen line is a decision worth making explicitly.

Each RC contributes to a cumulative set of release notes that will be finalized when `vX.Y.0` is released. After testing, if no critical issues remain, the last good rc is **promoted** to the regular release `vX.Y.0` — the exact rc images retagged by digest, never rebuilt and never tagged from a later commit (see [Regular Releases](#regular-releases)). Promotion cuts the write-once `vX.Y.0` tag at the promote PR's merge commit, fast-forwards the `release-X.Y` maintenance branch the freeze already created, and opens the way for patch releases.

## Regular Releases

A regular release `vX.Y.0` is **promoted from a release-candidate that already passed e2e** — never tagged by hand and never rebuilt. The bytes shipped as `vX.Y.0` are the exact `vX.Y.0-rc.N` images, retagged by digest. We'll use `v1.2.0` as an example.

```mermaid
gitGraph
    commit id: "feature"
    commit id: "feature 2"
    commit id: "feature 3" tag: "v1.2.0-rc.1"
```

A regular release sequence runs as follows:

1. Cut the first release candidate (`v1.2.0-rc.1`) from the last good commit on `main`: run the [`Cut Pre-release Tag`](../.github/workflows/cut-prerelease.yaml) workflow (manual dispatch **from `main`**) with the tag. It tags `main`'s HEAD as the CI app, then **creates `release-1.2` at that commit, freezing the line**; `tags.yaml` then builds the rc images, publishes the rc prerelease, pushes the digest-vendored `release-1.2.0-rc.N` staging branch, and runs its mandatory `rc-e2e` job by calling [`e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml) against that published tag.
2. Watch the rc's mandatory full E2E and wait for it to pass, then complete the other checks in [Pre-release verification checklist](#pre-release-verification-checklist). If the latest `tags.yaml` run cannot provide a green `rc-e2e`, a successful manual dispatch of [`e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml) for the same tag is equivalent gate evidence. `main` is already open for the next minor, so anything landing there is **not** in this release: fixes for `1.2` are cherry-picked onto `release-1.2`, and a later rc is cut by dispatching the workflow **from `release-1.2`**. **Optional:** once the notes are worth reviewing, press the `Generate RC Changelog` button ([`changelog-rc.yaml`](../.github/workflows/changelog-rc.yaml)) to generate `docs/changelogs/v1.2.0.md` now and bank it on the `release-1.2.0-rc.N` staging branch — so there is time to review and hand-edit it before promotion. Skipping the button changes nothing; promotion generates the changelog itself.

   ```mermaid
   gitGraph
       commit id: "feature"
       commit id: "feature 2"
       commit id: "feature 3" tag: "v1.2.0-rc.1"
       branch release-1.2
       checkout main
       commit id: "feature 4 (next minor)"
       checkout release-1.2
       commit id: "cherry-picked fix" tag: "v1.2.0-rc.2"
   ```

3. Once the checks are green, run the [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) workflow (manual dispatch) with that rc tag. Its `parse` job verifies E2E before any staging: it accepts a successful `rc-e2e` job in the latest `tags.yaml` push run for the tag, or any retained successful manual `e2e-tag.yaml` run whose expanded E2E job name targets the tag. The emergency `skip_e2e_gate` input bypasses this check, logs a loud warning, and marks the promote PR with ⚠️. Promotion is **transactional** — it performs no registry mutation at dispatch. It:
   1. Pushes the `release-1.2.0` branch: the rc's digest-vendored tree with the rc version substring rewritten to `1.2.0` (the digests are the rc's — unchanged).
   2. Drafts the `v1.2.0` release and uploads the (restamped) assets.
   3. Generates `docs/changelogs/v1.2.0.md` (in a parallel job, detached at the rc tag) — or, if you pressed the rc-time `Generate RC Changelog` button, copies the file already banked on the `release-1.2.0-rc.N` staging branch instead of regenerating — and commits it onto the `release-1.2.0` branch, so the changelog is reviewed as part of the promotion and finalize can publish the release with it already in place. Non-blocking: if generation fails the PR still opens, carrying a ⚠️ line that says the changelog is missing.
   4. Opens the `cozystack/website` "update managed apps reference" PR (`update-docs-v1.2.0` branch), generating the docs from the `release-1.2.0` staging branch via `FETCH_REF`. Non-blocking, and **do not merge that PR until `v1.2.0` is published** — merging early breaks the live site's version routing (see [Phase 3](#phase-3--update-website-docs-now-the-backstop)).
   5. Opens the `chore(release): promote v1.2.0-rc.N -> v1.2.0` PR into `release-1.2`, labelled `release` only. The PR body records whether E2E was verified or bypassed, plus ✅/⚠️ changelog and website-docs lines. E2E does not run on this promote PR by default because the immutable rc already passed the mandatory full suite; a maintainer may add `full-e2e` to run it again. The retag itself is deferred to the merge (step 5) — so an abandoned promotion leaves no stable-named images and cannot wedge a re-promotion.

   The PR targets the maintenance branch, not `main`: `promote-rc.yaml` prefers `release-X.Y` whenever it exists, and since the rc freeze it always does. The digest-vendored `Prepare release` commit therefore stays on the release line instead of landing on `main`.

   ```mermaid
   gitGraph
       commit id: "feature 3" tag: "v1.2.0-rc.1"
       branch release-1.2
       checkout release-1.2
       commit id: "cherry-picked fix" tag: "v1.2.0-rc.2"
       branch release-1.2.0
       checkout release-1.2.0
       commit id: "Prepare release v1.2.0"
       checkout release-1.2
       merge release-1.2.0 id: "Pull Request"
   ```

4. Maintainer reviews the PR and the draft release against the [Phase 4 checklist](#phase-4--review-and-merge-the-promote-pr): confirm the E2E-verification line is ✅ (or explicitly accept its ⚠️ bypass), the changelog line is ✅, and note the website-docs line — the `cozystack/website` docs PR that promotion parked, which stays unmerged for now. E2E does not run on this promote PR by default. Then merge — **do not squash-merge**, the stable tag must attach to a real merge commit. GitHub removes the merged branch `release-1.2.0` (the repo has auto-delete-on-merge enabled).
5. CI workflow triggers on merge (this is where every irreversible side effect happens, after the rc E2E gate and PR review):
   1. Creates the tag `v1.2.0` at the newly created merge commit — write-once. The tag is published here for the first time, never moved.
   2. Cuts the write-once `api/apps/v1alpha1/v1.2.0` Go-module tag at the same commit.
   3. Ensures the `release-1.2` maintenance branch exists at the tag commit. Since the rc freeze created it, this is normally a no-op fast-forward: the merge commit is already its tip.
   4. Publishes the release page (`draft` → `latest`), using `docs/changelogs/v1.2.0.md` from the merge commit as the release body.
   5. Retags the rc's images by digest to `v1.2.0` (and `:latest` only when `v1.2.0` is the newest published stable), and publishes the stable `cozy-installer` chart — no rebuild.
6. Once the release is published, merge the parked `cozystack/website` docs PR (opened back at promote time, step 3.iv) so the live site's version routing flips to `v1.2.0` — its versioned docs and `openapi.json` are on GitHub now. The maintainer can then announce the release to the community.

```mermaid
gitGraph
    commit id: "feature 2"
    commit id: "feature 3" tag: "v1.2.0-rc.1"
    branch release-1.2
    checkout main
    commit id: "feature 4 (next minor)"
    checkout release-1.2
    commit id: "cherry-picked fix" tag: "v1.2.0-rc.2"
    branch release-1.2.0
    checkout release-1.2.0
    commit id: "Prepare release v1.2.0"
    checkout release-1.2
    merge release-1.2.0 id: "Release v1.2.0" tag: "v1.2.0"
```

Note that `main` never receives the release commit. It forked away at the freeze and carries the next minor's work.

## Patch Releases

A patch release now follows exactly the same shape as the regular release above — by the time `vX.Y.0` ships, everything is already happening on `release-X.Y`. The only difference is where the line starts:

* `release-X.Y` already exists, created by the freeze at `vX.Y.0-rc.1`, so there is no branch to create.
* Patch commits are cherry-picked onto it, and the rc is cut by dispatching **from `release-X.Y`** — the same dispatch used for `rc.2` of the original release.


Let's assume that we've released `v1.2.0` and that development is ongoing.
We have introduced a couple of new features and some fixes to features that we have released 
in `v1.2.0`.

Once problems were found and fixed, a patch release is due.

```mermaid
gitGraph
    commit id: "feature 3" tag: "v1.2.0-rc.1"
    branch release-1.2
    checkout release-1.2
    commit id: "Release v1.2.0" tag: "v1.2.0"
    checkout main
    commit id: "feature 4"
    commit id: "patch 1"
    commit id: "feature 5"
    commit id: "patch 2"
```


1. The maintainer cherry-picks patch commits from `main` onto the existing `release-1.2` branch. These must be only patches to features that were present in version `v1.2.0`.

   Cherry-picking can be done as soon as each patch is merged into `main`, or directly before the release.

   The `backport` label automates this: [`backport.yaml`](../.github/workflows/backport.yaml) targets the newest existing `release-X.Y` branch, which during a freeze window is the line being stabilised rather than the last published one.

   ```mermaid
   gitGraph
       commit id: "feature 3" tag: "v1.2.0-rc.1"
       branch release-1.2
       checkout release-1.2
       commit id: "Release v1.2.0" tag: "v1.2.0"
       checkout main
       commit id: "feature 4"
       commit id: "patch 1"
       commit id: "feature 5"
       commit id: "patch 2"
       checkout release-1.2
       cherry-pick id: "patch 1"
       cherry-pick id: "patch 2"
   ```

   When all relevant patch commits are cherry-picked, the branch is ready for release.

2. The maintainer cuts a release candidate (`v1.2.1-rc.N`) via the [`Cut Pre-release Tag`](../.github/workflows/cut-prerelease.yaml) workflow (manual dispatch **from `release-1.2`**), which tags that branch's `HEAD`. CI builds and publishes the rc, pushes the `release-1.2.1-rc.N` staging branch, and runs the mandatory full `rc-e2e` job against the published tag.
3. Watch `rc-e2e` and wait for it to pass, then complete the other release checks. A successful manual `e2e-tag.yaml` run for `v1.2.1-rc.N` is also valid evidence if the latest `tags.yaml` run does not carry a green `rc-e2e`. **Optional:** press the `Generate RC Changelog` button ([`changelog-rc.yaml`](../.github/workflows/changelog-rc.yaml)) to generate `docs/changelogs/v1.2.1.md` at rc time and bank it on the rc staging branch; promotion then reuses it instead of regenerating.
4. Once the checks are green, run [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) (manual dispatch) with that rc tag. It runs the same five jobs as a regular promotion: the dispatch first gates on the latest tag-push `rc-e2e` or a successful tag-correlated manual E2E run (`skip_e2e_gate` overrides it), then pushes the `release-1.2.1` branch (rc digests, tag string rewritten to `1.2.1`), drafts the `v1.2.1` release with the restamped assets, generates or picks up `docs/changelogs/v1.2.1.md` and commits it onto that branch, opens the parked `cozystack/website` docs PR (updating the existing `v1.2/` directory in place, since a patch does not promote `next/`), and opens the promote PR into `release-1.2` with only the `release` label. The promote PR body carries the same ✅/⚠️ E2E, changelog, and website-docs lines and runs no E2E by default; add `full-e2e` manually to force a repeat. As with a regular release the retag is deferred to the merge — no registry mutation at dispatch.

   ```mermaid
   gitGraph
       commit id: "feature 3" tag: "v1.2.0-rc.1"
       branch release-1.2
       checkout release-1.2
       commit id: "Release v1.2.0" tag: "v1.2.0"
       checkout main
       commit id: "feature 4"
       commit id: "patch 1"
       commit id: "feature 5"
       commit id: "patch 2"
       checkout release-1.2
       cherry-pick id: "patch 1"
       cherry-pick id: "patch 2" tag: "v1.2.1-rc.1"
       branch release-1.2.1
       commit id: "Prepare release v1.2.1"
       checkout release-1.2
       merge release-1.2.1 id: "Pull request"
   ```

   Finally, when release is confirmed, the release sequence goes on.

5. Maintainer reviews the PR against the [Phase 4 checklist](#phase-4--review-and-merge-the-promote-pr), confirms the E2E-verification and changelog lines and notes the parked website-docs line, and merges it — **do not squash-merge**. GitHub removes the merged branch `release-1.2.1` (auto-delete-on-merge is enabled).
6. CI workflow triggers on merge (all irreversible side effects, after the rc E2E gate and PR review):
   1. Creates the tag `v1.2.1` at the newly created merge commit — write-once. The tag is published here for the first time, never moved.
   2. Cuts the write-once `api/apps/v1alpha1/v1.2.1` Go-module tag at the same commit.
   3. Publishes the release page (`draft` → `latest`; `latest` moves only if `v1.2.1` is the newest published stable).
   4. Retags the rc's images by digest to `v1.2.1` and publishes the stable `cozy-installer` chart — no rebuild.
7. Once the release is published, merge the parked `cozystack/website` docs PR, then announce the release to the community.

## What CI does during the release process

The numbered process above is implemented by seven workflows, including the reusable/manual [`e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml), plus the reusable [`changelog-generate.yaml`](../.github/workflows/changelog-generate.yaml) that two of them share to produce the changelog. Knowing which job does what makes the failure modes much easier to diagnose.

1. [`cut-prerelease.yaml`](../.github/workflows/cut-prerelease.yaml) — manual dispatch (from `main` or `release-X.Y`): the sole entry point for creating a pre-release tag. Validates the `-alpha`/`-beta`/`-rc` tag, then pushes it (write-once, at the branch tip) as the CI app so `tags.yaml` fires. Cutting the first `vX.Y.0-rc.N` also creates `release-X.Y`, freezing the line; afterwards a `vX.Y.*` dispatch from `main` is refused. Stable tags are never created here.
2. [`tags.yaml`](../.github/workflows/tags.yaml) — fires on any pre-release tag push: `prepare-release` builds and publishes the pre-release and pushes its digest-pinned staging branch, then — **only for `-rc.N` tags** — the mandatory `rc-e2e` job calls `e2e-tag.yaml` in the same run (`-alpha.N` / `-beta.N` skip it and are validated on demand with `e2e-tag.yaml`'s manual button). `generate-changelog` and `update-website-docs` also live here but are **backstops only** — both are normally produced at promotion time (see 4); on a stable tag push each self-skips or no-ops when the promote flow already did the work. These stable-tag backstops only fire because finalize pushes the stable tag with the CI app token (a `GITHUB_TOKEN` push triggers no run — see 5).
3. [`e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml) — reusable from `tags.yaml::rc-e2e` and manually dispatchable for a published tag. It resolves the tag's published disk plus digest-pinned tree and runs the full install, OpenAPI, and Chainsaw suite. Its dispatch run title is the constant workflow name, so the promote gate correlates a manual run through the successful tag-bearing E2E job name rather than the title.
4. [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) — manual dispatch. Five jobs: `parse` validates the rc tag and requires green full-E2E evidence before any staging; `promote` stages the `release-X.Y.Z` tree (rc digests, tag string rewritten to stable) and drafts the stable release; `changelog` calls the reusable `changelog-generate.yaml` **in parallel** to produce `docs/changelogs/vX.Y.Z.md` (regenerating it, or copying an rc-time changelog already committed to the `release-X.Y.Z-rc.N` staging branch by `changelog-rc.yaml` — see 7); `website-docs` (needs `parse`, `promote`) opens the `cozystack/website` docs PR from the staging branch via `FETCH_REF` (see [Phase 3](#phase-3--update-website-docs-now-the-backstop)); `open-pr` joins them, commits the changelog onto the staging branch, and opens the `release-X.Y.Z` promote PR with the `release` label but no automatic `full-e2e`. So the changelog and website docs are reviewed as part of the promotion, the already-checked rc suite is not repeated by default, and the PR body carries ✅ verified or ⚠️ bypassed plus ✅/⚠️ changelog and website-docs lines. Both `changelog` and `website-docs` are non-blocking. No registry mutation — transactional.
5. [`pull-requests-release.yaml`](../.github/workflows/pull-requests-release.yaml) — fires when the `release-X.Y.Z` PR merges; finalizes the release: cuts the write-once stable + Go-module tags, publishes the release **with the merged changelog as its body**, then retags the rc images to stable by digest (`:latest` gated on newest-stable) and publishes the stable chart.
6. [`update-releasenotes.yaml`](../.github/workflows/update-releasenotes.yaml) — fires on pushes to `main` that touch `docs/changelogs/v*.md`; syncs that content into the corresponding GitHub Release body. No longer the primary path (finalize sets the body directly, which is race-free and works for maintenance lines too) — this now covers later edits to a published changelog and manual re-syncs via `workflow_dispatch`.
7. [`changelog-rc.yaml`](../.github/workflows/changelog-rc.yaml) — manual dispatch, **optional**: generate the changelog at rc time instead of waiting for promotion, while there is still time to review it. It calls the same reusable [`changelog-generate.yaml`](../.github/workflows/changelog-generate.yaml) and commits the result to the `release-X.Y.Z-rc.N` staging branch as `docs/changelogs/vX.Y.Z.md`; the promote-time `changelog` job then copies that file instead of running the AI again. Skipping this workflow changes nothing — promote generates the changelog itself, exactly as before.

### Phase 1 — `prepare-release` (hard gate)

On an rc tag push: builds images, commits digest pins, creates the draft release, and pushes the `release-X.Y.Z-rc.N` staging branch. (The stable promote PR is opened later by `promote-rc.yaml`, not here. When the stable tag itself is pushed by finalize, a draft already exists, so this job is a no-op — see [Phase 5](#phase-5--pull-requests-releaseyaml-finalize).)

The commit (`Prepare release vX.Y.Z`, authored by `cozystack-ci[bot]`) is **digest pins and image tags only**:

- `packages/core/{installer,platform,testing}/values.yaml`
- `packages/system/*/values.yaml` (cozystack-api, dashboard, kamaji, linstor, kubevirt-csi-node, etc.)
- `packages/apps/kubernetes/images/{kubevirt-csi-driver,ubuntu-container-disk-*}.tag`
- `packages/system/dashboard/templates/configmap.yaml` (the `$tenantText` value)

Things that look surprising in the diff but are normal:

- **Zero-diff packages**: when buildx fully caches and upstream base images haven't moved, the digest is byte-identical to the previous release and won't appear. Verify the build step actually ran — don't assume "no diff = nothing built."
- **Sudden churn on `ubuntu-container-disk-*` tags**: `cloud-images.ubuntu.com/noble/current/` is a moving target; these often rebuild even without code changes.
- **Switched registry**: if a self-built image moved from `docker.io` to `ghcr.io`, the registry portion of the digest changes — that's a deliberate move, not a regression.
- **`targetVersion` is NOT touched by the release PR.** Platform migration `targetVersion` is bumped earlier in a feature commit (e.g. `[platform] Bump migration targetVersion to 39 for migration 38`). The release PR only re-pins the `platform-migrations` image digest.

### RC E2E — mandatory check before promotion

For an rc, `prepare-release` must finish before `tags.yaml::rc-e2e` calls the published-tag lane, so the full suite tests the published disk and digest-pinned staging tree that promotion will reuse. `promote-rc.yaml::parse` then checks the latest `tags.yaml` push run whose `head_branch` is the rc tag and accepts it only when the reusable E2E job succeeded. GitHub exposes called-workflow job names as `E2E Release Candidate / E2E vX.Y.Z-rc.N (full suite)`, which the gate matches by the exact tag-bearing suffix.

The alternate evidence path scans retained successful `workflow_dispatch` runs of `e2e-tag.yaml` and accepts one whose direct E2E job is named `E2E vX.Y.Z-rc.N (full suite)`. The workflow's run title is always `E2E Release Tag` and therefore cannot correlate the input tag; artifact names also carry the tag but expire independently, so the job name is the strongest available correlator. If Actions retention or manual deletion removes the run/job metadata, or that job is renamed without updating the matcher, the gate fails closed and tells the maintainer to run the E2E Release Tag button again. `skip_e2e_gate=true` is an emergency override, not alternate evidence: it writes a loud warning and a ⚠️ PR-body line.

### Phase 2 — the changelog (generated at rc time or promotion, not here)

The changelog for `vX.Y.Z` is produced by [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml)'s `changelog` job (a thin call to the reusable [`changelog-generate.yaml`](../.github/workflows/changelog-generate.yaml)) and committed onto the `release-X.Y.Z` staging branch by `open-pr`, so it rides the promote PR and is on the base branch the moment the release publishes. Finalize then uses that file verbatim as the GitHub Release body. Generating it here — on the stable tag push — would be too late by construction: that tag is created by finalize, which has already published the release. It can also be produced earlier, at rc time, via the optional [`changelog-rc.yaml`](../.github/workflows/changelog-rc.yaml) button — which runs the same reusable core and commits the file to the `release-X.Y.Z-rc.N` staging branch, from which the promote-time `changelog` job copies it (validated) instead of regenerating.

The `changelog` job runs **in parallel** with `promote`, detached at the rc tag (the "promotion" configuration in §2 of [`agents/changelog.md`](./agents/changelog.md)), using a separate **read-only** GitHub App token for the AI step so the model cannot mutate the repo even with `--allow-all-tools`. Every write — the commit onto the staging branch and the PR — happens in `open-pr` under a write-scoped token.

Generation is deliberately **non-blocking**: a Copilot outage or exhausted token quota must never block a release. Only the AI step carries `continue-on-error`, and `open-pr` is gated on `promote` alone (`if: !cancelled() && needs.promote.result == 'success'`) so the promote PR opens regardless. When the changelog is missing the PR body says so explicitly — that ⚠️ line is the signal to act on.

Recovery, in order of preference:

1. Commit `docs/changelogs/vX.Y.Z.md` to the `release-X.Y.Z` branch by hand (follow [`agents/changelog.md`](./agents/changelog.md)). `promote` preserves it across a re-dispatch, and `open-pr` will not overwrite it with generated output.
2. Re-dispatch `promote-rc.yaml` — generation runs again; anything already on the branch wins.
3. Merge without one and let `tags.yaml::generate-changelog` (the backstop) open a changelog PR against `main` after the fact. The release is published with the draft's body until that merges.

Known failure modes:

- Copilot quota exhausted (`COPILOT_GITHUB_TOKEN` 402). Refill or rotate, then re-dispatch.
- AI step timeout (hard 30-min cap).
- Output truncated or whitespace-only — caught by the `Verify changelog` step (`hack/validate-changelog.sh`), which accepts either header convention (`# Cozystack vX.Y.Z` for a minor or `# vX.Y.Z (<date>)` for a patch), and requires the leading release-link comment pointing at `releases/tag/vX.Y.Z`, a `compare/...vX.Y.Z` link ending in this version, and at least one `## ` section — deliberately with no line-count floor, since a complete short patch changelog (v1.5.1 ships in 19 lines) must not be rejected as a fragment. It downgrades to the missing-changelog path rather than shipping a fragment as release notes.

**`tags.yaml::generate-changelog` is the backstop.** It self-skips when the changelog is already on `main`. It earns its keep in two cases: generation failed during promotion, or the promote PR targeted `release-X.Y` so the file never reached `main`. Since the rc freeze the second case is **every** release, minor and patch alike — the promote PR always targets the release line (see [Regular Releases](#regular-releases)), which makes this backstop the only route by which a published changelog reaches `main`. In that case it **ports the reviewed file from the tag commit verbatim** rather than regenerating — a second AI pass there would spend quota and then overwrite already-published, already-reviewed release notes.

### Phase 3 — `update-website-docs` (now the backstop)

The `cozystack/website` "update managed apps reference" PR is normally opened **earlier, at promote time**, by [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml)'s `website-docs` job — not here. That job fetches the README content from the `release-X.Y.Z` staging branch `promote` just pushed (via the website Makefile's `FETCH_REF` variable), so the docs are ready to review alongside the promote PR while the stable tag is still unborn. It refreshes the trunk version pins from the staging ref before promoting `next/` → `vX.Y/`, so a new minor's docs inherit fresh Talos/cozystack pins instead of a stale `next.yaml`. It is **non-blocking**: if it fails (or trips its `FETCH_REF`-support guard against a website checkout that predates that Makefile variable), the promote PR opens with a ⚠️ website-docs line and this tag-time job backfills the same branch.

**Merge-timing contract:** the website PR must NOT be merged before the release is published (finalize). Merging it early flips the site's latest-version pointer to a version whose versioned docs and `openapi.json` are not on GitHub yet, so every version-pin and API link for the new version 404s. Both the website PR body and the promote PR body say so; park the website PR until the GitHub release for `vX.Y.Z` is live, then merge.

This tag-time `update-website-docs` job stays as the **backstop**. It runs with `if: !cancelled() && needs.prepare-release.result == 'success'` (surviving a failed/skipped changelog phase, stopping on cancel), decides whether to promote `next/` → `vX.Y/`, runs `make update-all`, stages `content hugo.yaml data/versions`, and force-pushes the same `update-docs-vX.Y.Z` branch. On the normal path — where `promote-rc.yaml::website-docs` already did the work — it no-ops into "No changes to commit". At tag time the stable tag exists, so its `BRANCH=RELEASE_TAG` fetch resolves without needing `FETCH_REF`. For it to fire at all, finalize must push the stable tag with the **CI app token** (see [Phase 5](#phase-5--pull-requests-releaseyaml-finalize)) — a tag pushed with the default `GITHUB_TOKEN` triggers no workflow run.

If anyone changes the website Makefile to write somewhere else (e.g. `static/`, `i18n/`), the `git add` list must grow in **both** jobs or the PR silently drops files.

### Phase 4 — review and merge the promote PR

Reviewer checklist for the `chore(release): promote vX.Y.Z-rc.N -> vX.Y.Z` PR:

- [ ] The PR body says `✅ RC full e2e was verified`; if it says the gate was bypassed, obtain explicit maintainer acceptance or add `full-e2e` and wait for that opt-in run.
- [ ] The PR body's **website-docs line** is present: if ✅, the `cozystack/website` docs PR is open and **parked** — do NOT merge it until `vX.Y.Z` is published (see [Phase 3](#phase-3--update-website-docs-now-the-backstop)); if ⚠️, the tag-time backstop will backfill it after finalize.
- [ ] Diff is digest pins + image tags only — nothing else.
- [ ] No accidental config drift (a value file you don't recognize).
- [ ] If any chart bumped, check the new digest pulls — `crane manifest ghcr.io/cozystack/cozystack/<name>@<digest>` should return.
- [ ] Build artifacts on the draft release page exist and match the expected count.
- [ ] The PR includes `docs/changelogs/vX.Y.Z.md`, it has content, and the entries match the commit range (see [Changelog pre-publish verification](#changelog-pre-publish-verification)). If the PR body carries the ⚠️ "No changelog in this PR" line, resolve that before merging — finalize publishes this file as the release body.

### Phase 5 — `pull-requests-release.yaml` (finalize)

Fires on merge of a PR that is merged, carries the `release` label, and is authored by `cozystack-ci[bot]` (the last guard closes the "name a branch `release-X.Y.Z`, get it labelled, merge it" hole). Head branch must match `release-X.Y.Z[-suffix]`. This is where **every irreversible side effect of a promotion happens** — after the rc's mandatory E2E was checked at dispatch and the PR merged, making promotion transactional. Steps:

1. **Create the tag at the merge commit** (write-once). The merge commit of `Prepare release vX.Y.Z` did not exist before the PR opened, so there is nothing to move — the tag is created here for the first time. A pre-existing tag at a different commit fails the step loudly rather than being force-moved (see [Tag immutability](#tag-immutability)). The checkout uses `persist-credentials: false` so this push authenticates as the **CI app** (via `git remote set-url`), not the default `GITHUB_TOKEN` — a `GITHUB_TOKEN` push creates no workflow run, so a persisted-credential push would silently skip `tags.yaml` entirely and its stable-tag backstops (`generate-changelog`, `update-website-docs`) would never fire.
2. **Cut the `api/apps/v1alpha1/vX.Y.Z` Go-module tag** (write-once, stable only) at the same commit, so Go consumers of `api/apps/v1alpha1` get the release. Moved here from `tags.yaml`, whose `prepare-release` body is skipped for a promoted stable (the draft already exists).
3. **Ensure the maintenance branch `release-X.Y` exists** at the tag commit. Since the rc freeze creates it, this is normally a no-op fast-forward — the merge commit is already the branch tip. Still created if missing; updated fast-forward-only — a non-fast-forward update warns and is left for a maintainer rather than being force-updated.
4. **Publish the draft release**, with `docs/changelogs/vX.Y.Z.md` from the merge commit as its body (an absent or whitespace-only file warns and leaves the draft's body in place, rather than publishing blank notes). `make_latest` is computed against published-non-prerelease tags: prereleases stay `false`; tags older than the current max stay `false` (and the current max is force-restored to `latest` if necessary, so an older patch tag cut after a newer minor won't downgrade `latest`).
5. **Retag the rc images to stable** by digest (`hack/promote-retag.sh`, no rebuild) and **publish the stable `cozy-installer` chart**. `:latest` (on both the images and the chart) moves only when this release's `make_latest` was `true` — the same decision as step 4, so the release's `latest` and the images' `:latest` never disagree. The chart is packaged with `platformVersion` stamped into its default values so the documented `helm --install --version X.Y.Z` path reports the stable version.

### Phase 6 — `update-releasenotes.yaml` (sync GitHub Release body)

Fires on pushes to `main` that touch `docs/changelogs/v*.md`. Reads each `docs/changelogs/vX.Y.Z.md` and PATCHes the matching GitHub Release's `body` if it differs. Since Phase 5 now sets the body directly from the merge commit, this is no longer on the critical path — it covers later **edits** to a published changelog, the backstop PR landing a changelog that reached `main` after the fact, and manual re-syncs via `workflow_dispatch`. It only watches `main`, which is why finalize (not this job) is what populates a release promoted onto `release-X.Y` — since the freeze, that is every release rather than only a patch.

## Stable tags come from rc promotion

A stable `vX.Y.Z` is created only by **promoting an existing release-candidate** that already passed e2e — never by rebuilding and never by a cron.

- [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) is triggered manually once an rc has gone green. It rewrites the rc version substring in the vendored image tags (the digests stay the rc's) and opens the `release-X.Y.Z` staging PR — no registry mutation. Merging that PR (Phase 5) creates the write-once stable tag at the merge commit, then retags the rc's already-built, e2e-passed image digests to the stable tag **by digest** (no rebuild — see [`hack/promote-retag.sh`](../hack/promote-retag.sh)) and publishes the release. Because the copy source is the immutable rc digest, the stable image is bit-for-bit the rc image that passed e2e — and because the retag runs only after merge, an abandoned promotion never leaves stable-named bytes behind.

So a commit on a supported `release-X.Y` line ships when a maintainer promotes the next rc for that line — not automatically within 24h. Pushing a stable `vX.Y.Z` tag by hand is **not** a supported path: `tags.yaml` fails fast on a stable tag that has no pre-existing draft ("stable tags come from promote-rc.yaml"), and even so the finalize step would refuse to move a pre-existing tag. Pre-releases (`vX.Y.Z-rc.N`, `-alpha.N`, `-beta.N`) are cut with the [`Cut Pre-release Tag`](../.github/workflows/cut-prerelease.yaml) workflow, which pushes the tag as the CI app; [`tags.yaml`](../.github/workflows/tags.yaml) then fires on that push, builds it, and publishes the pre-release. Cutting via the workflow (rather than a manual `git push`) is what lets repo admins lock `v*` tag creation to the CI app — see the tag-protection note below.

## Nightly builds

A nightly is an **installable copy of `main` on GHCR — not a rebuild and not a release**. [`build-main.yaml`](../.github/workflows/build-main.yaml) already builds every push to `main` into the CI registry (OCIR); the nightly promotes that build to the public release registry (GHCR) and proves it installs.

[`nightly.yaml`](../.github/workflows/nightly.yaml) runs daily (gated by the `NIGHTLY_ENABLED` repo variable) in three stages:

1. **mirror** — resolve the exact commit `cozystack-packages:main` was built from, then [`hack/nightly-mirror.sh`](../hack/nightly-mirror.sh) copies every cozystack-owned component image OCIR→GHCR **by digest** (bit-for-bit, no rebuild) and re-publishes the rewritten `cozystack-packages` artifact and the `cozy-installer` chart to GHCR, tagged `0.0.0-nightly.<YYYYMMDD>.<run-id>.<attempt>` plus a floating `nightly`.
2. **build-disk** — assemble the Talos `nocloud` disk from the upstream siderolabs imager (the profile references only `ghcr.io/siderolabs/*`, so this needs no cozystack image and no rebuild) and publish it as a GHCR OCI artifact (`cozystack-nocloud`) so a nightly is installable on real hardware, not just in e2e.
3. **e2e** — stage the published GHCR closure (rewritten tree + the `cozy-installer` chart pinned to the GHCR packages artifact + the disk) and run the **full** app suite. A nightly has no diff, so Test Impact Analysis does not apply.

No GitHub release and no `api/apps/v1alpha1/*` Go-module tag are created for a nightly. [`retention.yaml`](../.github/workflows/retention.yaml) prunes old GHCR nightly versions (keeps the newest 14 per package; the floating `nightly`, release tags and untagged versions are never touched).

Install a nightly:

```bash
helm upgrade --install cozystack \
  oci://ghcr.io/cozystack/cozystack/cozy-installer --version 0.0.0-nightly.20260626.15992304129.1
# ...or --version nightly for the latest
```

The matching Talos node image is `ghcr.io/cozystack/cozystack/cozystack-nocloud:0.0.0-nightly.20260626.15992304129.1` (pull with `oras`).

## Backports

### The backport bot

[`backport.yaml`](../.github/workflows/backport.yaml) wraps [`korthout/backport-action`](https://github.com/korthout/backport-action) and fires on a merged `main`-targeted PR carrying one of:

| Label | Target branch |
|-------|---------------|
| `backport` | the newest existing `release-X.Y` branch |
| `backport-previous` | the second-newest existing `release-X.Y` branch |

Resolution is dynamic at run time, and it reads the branches themselves: the job lists the repository's branches, keeps the ones matching `release-<major>.<minor>`, and sorts them numerically descending — so `release-1.10` ranks above `release-1.9`, which a lexicographic sort gets backwards. `backport` takes the first, `backport-previous` the second, so both name a branch that exists by construction; asking for `backport-previous` when only one line exists fails the job rather than inventing a target. Nothing is derived arithmetically — an earlier version computed the previous line as `Y-1`, which named a non-existent branch whenever a minor was skipped.

**During a freeze window the newest line is the one being stabilised, not the last published one.** `release-X.Y` is created when the first `vX.Y.0-rc.1` is cut, before `vX.Y.0` is published, so from the freeze until the release `backport` targets the release being stabilised and `backport-previous` targets the last published stable. That is the intent: the frozen branch is the only way into the upcoming release, which is when backports matter most. The trade-off is that for the length of the window the line one step further back has no label pointing at it, so a fix that has to reach it before the new release publishes must be cherry-picked by hand.

The bot creates a backport PR with title `[Backport release-X.Y] <original title>`. When this PR merges, the title prefix used to re-trigger the bot through `pr-labeler.yaml`, which auto-applied `backport` to any `[Backport release-X.Y]`-titled PR. Combined with the org-level `dosubot` re-applying the label, this caused recursive backports.

The fix (PR #2584): both job `if:` blocks gate on `github.event.pull_request.base.ref == 'main'`. Backport PRs target `release-X.Y`, so they cannot satisfy this — architectural protection, regardless of which bot relabels them.

### When the bot fails

Conflicting cherry-picks produce a draft PR via `conflict_resolution: draft_commit_conflicts`. Look for the bot's comment with the merge-conflict diff. You either:

- Resolve in the draft branch and undraft, or
- Drop the bot's branch and cherry-pick manually:

```bash
git checkout release-X.Y
git cherry-pick -x -s <commit-sha>
# resolve conflicts
git commit -s
git push origin release-X.Y  # or push to a new branch and open a PR
```

To find the bot's failed comments across a batch of PRs:

```bash
for n in $(gh pr list --search "label:backport label:backport-previous merged:>=2026-01-01" --json number --jq '.[].number'); do
  echo "=== #$n ==="
  gh pr view $n --json comments --jq '.comments[] | select(.author.login == "github-actions" or (.author.login | contains("backport"))) | .body' | head -20
done
```

### Cherry-pick triage before a patch

A patch release includes bugfixes for code that shipped in the corresponding minor `vX.Y.0`. Use:

```bash
# 1. Inventory PRs already labeled for backport (merged but not yet on release-X.Y)
gh pr list --search "is:merged label:backport" --limit 100
gh pr list --search "is:merged label:backport-previous" --limit 100

# 2. List commits on main since the release branch diverged that are NOT yet on release-X.Y
git merge-base origin/main origin/release-X.Y
git log <base>..origin/main --grep="(#" --oneline

# 3. Open PRs that may need labeling before the cut
gh pr list --state open --base main --label kind/bug
```

**Include rule:**

- `kind/bug` — especially destructive (data loss, crash-loop, OOM, eviction).
- `area/security` / CVE.
- Narrow chart fixes whose blast radius is contained (e.g. one operator's resources).
- Patch-line-policy-matching dependency bumps (e.g. cilium 1.X.Y → 1.X.(Y+1) on a `release-A.B` shipping cilium 1.X — never a minor bump, only a patch within the line the branch already ships).

**Skip rule:**

- `kind/feature` — even with `lgtm`. A patch release is not a delivery vehicle for features.
- CI-only changes (`area/ci`, test infrastructure).
- Docs-only changes (those land via `update-website-docs` automatically).
- Large multi-package dependency churn (Dependabot Go-deps bumps).
- PRs whose conventional-commit type is `!` (breaking) or that mix `feat` with `fix`.

**Borderline cases:**

- Large refactors with `lgtm` AND an obvious bug payload — prefer extracting the bug fix into a narrower commit and backporting only that.
- PRs labeled both `kind/bug` and `kind/feature` — split them.

### Backporting code that touches monotonic counters

Migrations are the canonical case. `packages/core/platform/images/migrations/run-migrations.sh` is a linear counter — it runs `seq $CURRENT_VERSION $((TARGET_VERSION - 1))` and stops if `CURRENT >= TARGET`. This creates a forward-upgrade trap. Walking through it with `release-1.2` as the maintenance branch:

- `main` has `targetVersion=10` and migrations 1–9 on disk.
- The `release-1.2` branch was cut when `targetVersion=5` (migrations 1–4 on disk).
- You backport a new migration to `release-1.2` at slot 5, and naively bump `targetVersion` 5 → 6 to enable it.
- Customer running 1.2.x runs the new migration, stamps `cozystack-version=6` in-cluster.
- On upgrade to 1.3 (main's `targetVersion=10`), the runner walks `seq 6 9` and runs main's migrations 6–9 — **main's own migration at slot 5 never runs.** The backport claimed that slot on `release-1.2`; main's slot 5 holds an unrelated migration that gets silently skipped.

Mitigations, in order of preference:

1. **Don't bump `targetVersion` in the backport.** Just ship the migration file. Prior precedent in the repo: an earlier maintenance branch's ACME backport did exactly this — the migration shipped but ran on no clusters until a later commit raised the counter. Pair this with relaxing the chart-side `{{ fail }}` guard on the maintenance branch (chart-side coalesce) so the new value works without the migration having executed.
2. **If you must bump,** add an idempotent duplicate of the now-skipped main migration at a higher slot. Bounded cost (one extra file per affected migration) and survives the skip window.
3. **Audit cross-branch counter state before deciding.** Compare migration numbering across `main`, `release-X.Y`, and `release-X.(Y-1)`. If a backport would create a skip window on any forward-upgrade path, choose option 1.

This rule generalizes to any monotonic-counter state: schema versions, feature-flag generations, anything that gates "have I run X yet?" with a single integer.

## Changelogs

### Where the canonical process lives

[`agents/changelog.md`](./agents/changelog.md) is the source of truth. Read it end-to-end before generating; do not infer the process from past commits or memory. The CI runs it under Copilot via the reusable `changelog-generate.yaml` — called by `promote-rc.yaml::changelog` at promote time and by the optional `changelog-rc.yaml` at rc time — and, as a backstop, `tags.yaml::generate-changelog`; locally the same prompt is replayable by following the doc directly.

### Common changelog failure modes

These are mistakes that have shipped in real changelogs. Verify against each before merging the changelog PR.

**1. Commits from outside the release range.** Caused by running `git log <prev>..HEAD` from `main` while generating a patch changelog — `HEAD` on `main` contains everything that landed since the tag plus backports merged to `release-X.Y` after the tag was cut. Use `git log <prev>..<new_tag>` explicitly. The doc was updated to require this after v1.3.1's changelog shipped with 14 entries for what was actually a 1-commit release.

**2. Original + backport listed as two separate entries.** Each backport must coalesce with its original into a single entry of the form `(in #orig, backport #bp)`. To verify after generation:

```bash
# Every entry that mentions "backport" should also reference the original PR number
grep -E 'backport #[0-9]+' docs/changelogs/v<new>.md | grep -v '#[0-9]*, backport'
```

Output should be empty.

**3. Wrong PR author.** The squash-merge commit's author is whoever clicked "Merge" — not the person who wrote the code. **Always** resolve via `gh pr view <N> --json author --jq .author.login`, never `git log --format=%an`. This bites hardest for website-repo entries where the same merger handles most PRs.

**4. Superseding patch, stale changelog.** Tags are write-once, so a critical fix after `vX.Y.Z` ships as the next patch `vX.Y.(Z+1)` rather than moving the tag. If a changelog PR already exists for a tag you are superseding, the changelog work transfers to the new patch. Compare `git log <prev>..<new>` against what the existing `changelog-vX.Y.Z` branch already documents; only add/remove the deltas. If the only new commits are CI-internal or a revert of a feature that never reached a stable tag, no changelog edit is needed.

**5. PR numbers swapped inside prose.** The entry-format validator checks bullet entries but ignores Feature Highlights paragraphs and Upgrade Notes. Both have caused wrong PR references in shipped changelogs. Verify every `#NNNN` in prose with `gh pr view <N>`.

**6. Hallucinated entries.** Every entry must correspond to a commit in `git log <prev>..<new>`. Verify by extracting PR numbers from the file and grep-searching the commit range:

```bash
grep -oE '#[0-9]+' docs/changelogs/v<new>.md | sort -u | while read pr; do
  n=${pr#\#}
  if ! git log <prev>..<new> --grep="#${n}" --oneline | grep -q .; then
    echo "PR $pr in changelog but not in commit range"
  fi
done
```

**7. Bot accounts as human contributors.** `cozystack-ci[bot]`, `github-actions`, `dependabot`, `renovate`, and any `app/*` login must NOT appear in the `## Contributors` list. They legitimately appear in **per-entry attribution** (e.g. a Renovate-authored PR) — but not in the human roll-call.

**8. Title duplicated as description.** `* **fix(foo): X**: fix(foo): X (...)` means you wrote the conventional-commit subject twice and never wrote a user-facing description. The brief and the detail must say different things — the detail explains what the change means for users.

### Changelog pre-publish verification

Run these checks before merging the changelog PR. Catches problems in roughly the order they occur:

```bash
PREV=v1.2.0; NEW=v1.2.1

# A. Every chart bump is mentioned somewhere in the changelog
git diff --name-only $PREV..$NEW -- 'packages/*/charts/*/Chart.yaml' 'packages/*/*/charts/*/Chart.yaml' | while read f; do
  old=$(git show "$PREV:$f" 2>/dev/null | yq '.version // ""')
  new=$(git show "$NEW:$f" 2>/dev/null | yq '.version // ""')
  if [ "$old" != "$new" ]; then
    component=$(basename "$(dirname "$f")")
    if ! grep -qi "$component" docs/changelogs/$NEW.md; then
      echo "MISSING: $component $old -> $new ($f)"
    fi
  fi
done

# B. Out-of-Chart.yaml pins (Talos, tenant K8s, vendored images)
for path in packages/core/talos/values.yaml packages/core/installer/values.yaml packages/apps/kubernetes/values.yaml; do
  git diff $PREV..$NEW -- "$path" | head -40
done
git diff --name-only $PREV..$NEW -- 'images/*/Dockerfile'

# C. Every cited #NNNN exists and is a merged PR (not an issue)
grep -oE '#[0-9]+' docs/changelogs/$NEW.md | sort -u | sed 's/#//' | xargs -P 20 -I{} sh -c '
  state=$(gh api /repos/cozystack/cozystack/issues/{} --jq ".pull_request.merged_at // \"ISSUE\"" 2>/dev/null)
  echo "#{} $state"
' | grep -E 'ISSUE|null|^#[0-9]+ $'

# D. URL liveness
grep -oE 'https://[^)" ]+' docs/changelogs/$NEW.md | sort -u | xargs -P 10 -I{} sh -c '
  code=$(curl -s -o /dev/null -w "%{http_code}" {})
  if [ "$code" != "200" ]; then echo "$code {}"; fi
'

# E. Website PR authors are PR authors, not commit authors
grep -oE 'cozystack/website#[0-9]+' docs/changelogs/$NEW.md | sort -u | sed 's|cozystack/website#||' | while read n; do
  pr_author=$(gh pr view $n --repo cozystack/website --json author --jq .author.login)
  echo "website#$n -> @$pr_author"
done

# F. Working tree clean except for the changelog
git status --porcelain | grep -v "docs/changelogs/$NEW.md"
# Should output nothing.
```

Ship criterion: every chart bump surfaced, no issue-cited-as-PR, no 404 URLs, every website entry attributed via `gh pr view` (not commit author), `git status` shows only the changelog.

### Cozystack-specific URL quirks

`cozystack.io` paths are versioned: `/docs/operations/foo/` and `/docs/next/foo/` both 404. Only `/docs/vX.Y/foo/` and the unversioned canonical `/docs/foo/` serve content. AI-generated changelogs invent the unversioned form. Verify URLs with the `curl` loop above.

### Platform-component coverage

For a minor release (`vX.Y.0`), changelog entries should surface what an upstream chart bump actually brought in — features, breaking changes, security fixes. PR commit messages usually don't capture this; the upstream `CHANGELOG.md` or GitHub Release notes do. For each bumped `Chart.yaml`:

1. Read the `sources:` field — typically a GitHub URL.
2. Fetch release notes strictly inside `old < x <= new` via `gh release view <tag> --repo <owner>/<repo> --json body,name`.
3. Summarize in 2–5 user-impact bullets per component (new features, breaking changes, CVEs, important deprecations). Don't reproduce upstream notes verbatim; link to them.
4. If a PR-level entry already mentions the bump, enrich that entry with bullets — don't duplicate the entry.
5. If 3+ bumps have no PR-level mention, introduce a `## Platform Components` section.

If you can't find upstream notes for a bumped component, list it with version-only info and no bullets. Hallucinated bullets are worse than missing detail.

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
- [ ] **The promote PR carries `docs/changelogs/vX.Y.Z.md` and it was verified** per [Changelog pre-publish verification](#changelog-pre-publish-verification). Finalize publishes this file as the release body, so verifying it before merge is verifying the release notes.
- [ ] **The `cozystack/website` docs PR stays parked until publish.** Promotion opens it early; merging it before the GitHub release for `vX.Y.Z` is live 404s the new version's docs and API links. Merge it right after finalize publishes the release (regular-release step 6, patch step 7).

## CI failures release engineers commonly hit

| Symptom | Class | Diagnosis | Block release? |
|---------|-------|-----------|----------------|
| `Draft release for vX.Y.Z not found` on every release-* merge | Workflow regression — `Publish draft release` lost its `github-token` in a past refactor; default `GITHUB_TOKEN` cannot list drafts | At `pull-requests-release.yaml:163`. Undraft manually, file the fix | No |
| Changelog Copilot job returns 402 | Token quota — `COPILOT_GITHUB_TOKEN` premium-request quota empty | Both changelog entry points hit it: the optional rc-time `changelog-rc` dispatch and the promote-time `changelog` job (both call the reusable `changelog-generate.yaml`), plus the `tags.yaml` backstop | No — `continue-on-error: true` on the AI step, generate changelog by hand following [`agents/changelog.md`](./agents/changelog.md) |
| E2E `kubernetes-test` fails with `CSINode does not contain driver csi.kubevirt.io` | Test ordering bug | Tenant's `kubernetes-${test_name}-csi` HelmRelease still installing when NFS PVC is created. Fix: wait for `csinode/<node>` to advertise the driver before creating the PVC | Yes — flaky and masks real CSI bugs |
| `tenant-root` HR in perpetual upgrade-rollback loop after OIDC patch | Real bug at the intersection of #2602's readiness change and the cozystack reconcile cadence | Look for `rate: Wait(n=1) would exceed context deadline` in operator logs; multiple tenant child HRs stuck `InProgress` | Yes |
| `cozy-dashboard` ImagePullBackOff from `cozystack-ui:latest` | Registry flake | Known noisy on some OCI mirrors; not currently in the prepull set | No — retry |
| PR CI: `docker push` 409 on `:latest` for the same image across two PRs | Concurrent manifest race on a shared floating tag | The `:latest` tag was previously written by every `make image` regardless of PR. Resolved by the image-tag refactor (`IMAGE_TAG=pr-<N>-<sha>` in PR builds, `IMAGE_TAG=<ref_name>` only at release time) | No — `gh run rerun --failed` |

**Heuristic:** workflow-token and Copilot-quota issues are out-of-band — release the tag, fix the workflow in a follow-up PR. Test ordering bugs and tenant-root reconcile storms are real and should block.

If you find yourself doing the same manual fixup on two consecutive releases (e.g. "undraft the release"), open a workflow-regression issue. Workflow bugs with known manual workarounds rot silently for months.

## Tag immutability

Published tags are **write-once** — once a `vX.Y.Z` or rc tag is pushed it is never moved or deleted. (Nightlies are not git tags at all — they are GHCR OCI tags, see [Nightly builds](#nightly-builds).) Moving a tag silently poisons the Go module proxy / pkg.go.dev cache for `api/apps/v1alpha1/vX.Y.Z` and drifts SBOM/provenance toolchains, so the release flow is built so a move is impossible by construction:

| File | Tag / branch handling |
|------|------------------------|
| [`pull-requests-release.yaml`](../.github/workflows/pull-requests-release.yaml) | Creates `vX.Y.Z` at the PR merge commit **write-once** (create if absent, no-op if unchanged, fail loudly if it would move — the merge commit is new, so there is nothing to move). Cuts the `api/apps/v1alpha1/<vTAG>` Go-submodule tag write-once at the same commit, **stable only** (never rc/beta/alpha). The `release-X.Y` maintenance branch is fast-forward-only. Retags the rc image digests to the stable image tag (also write-once at the image level). |
| [`tags.yaml`](../.github/workflows/tags.yaml) | The `release-X.Y.Z-rc.N` staging branch is a mutable staging ref (compare-before-force: skipped when unchanged, force+log only when it genuinely moves). Fails fast on a stable `vX.Y.Z` tag with no pre-existing draft — stable tags come from `promote-rc.yaml`, never a hand push. |
| [`promote-rc.yaml`](../.github/workflows/promote-rc.yaml) | Mutates no tags. Before staging, verifies green full E2E from the latest rc tag-push run or a tag-correlated manual published-tag run (unless loudly bypassed). Stages the `release-X.Y.Z` branch, generates the changelog onto it, and opens the promote PR with `release` but no automatic `full-e2e`; the stable tag and image retag both happen at merge (finalize). The staging branch is force-pushed on re-dispatch, but an existing `docs/changelogs/vX.Y.Z.md` is preserved across the rebuild. |

This is the immutable-tag + rc-promotion model from [#2677](https://github.com/cozystack/cozystack/issues/2677): stable `vX.Y.Z` is created only by promoting an existing rc, rc tags are write-once, and `api/apps/v1alpha1/vX.Y.Z` is created only on a stable release. The old nightly `auto-release.yaml` (which delete-recreated auto-bumped patch tags) has been removed. rc and stable tags accrete permanently; the only churning artifacts are GHCR nightlies, which [`retention.yaml`](../.github/workflows/retention.yaml) prunes (see [Nightly builds](#nightly-builds)) — GHCR storage is the cost vector to watch.

### Repo-side enforcement (tag-protection rulesets)

The guarantees above are enforced in the workflows. Repo admins back them with two GitHub **tag rulesets** targeting `v*`:

- **Immutability** — *Restrict updates* + *Restrict deletions*, **no bypass**. No actor (not even the CI app) can move or delete a shipped `v*` tag. Safe to enable at any time: nothing in the release flow ever moves or deletes a `v*` tag, it only creates new ones.
- **Creation control** — *Restrict creations* with the **CI app as the sole bypass actor**, plus *Limit branches/tags updated in a single push* = 1. This turns "don't hand-push tags" from convention into hard enforcement: every `v*` tag — pre-release **and** stable — can then be created only by CI. Enable this **only after** [`cut-prerelease.yaml`](../.github/workflows/cut-prerelease.yaml) is in use, since that workflow is what makes CI the sole creator of pre-release tags; enabling it earlier would lock maintainers out of cutting rc/alpha/beta. (The Go-module tag `api/apps/v1alpha1/*` gets an equivalent pair of rulesets.)

## Splitting a release-blocking bundle PR

Sometimes the work that has to land before a release is a 40-commit grab bag (CI stabilization, dependency bumps, chart fixes). Splitting it makes it reviewable and survivable. The strategy that has worked:

1. **Verify what's already on `main`.** Use `git merge-base --is-ancestor` and subject grep to drop commits that have already landed.
2. **Find existing open PRs that cover the same commits.** Force-push to the existing branch instead of opening a duplicate — preserves review threads.
3. **Identify the "bottleneck" PR.** The one whose changes make previously-silent misconfigurations into hard errors (typically an operator-version bump). Everything else can fan out in parallel.
4. **Squash iterative same-author bug-on-bug fixes** inside each split. Preserve authorship via `--author` on the squashed commit.
5. **Mark the keystone PR as draft** and link in the body to the dependency PRs. Reviewers know it intentionally fails CI until the chain lands.
6. **Beware of concurrent registry pushes.** When fanning out 5+ PR CI runs simultaneously, multiple `make image` jobs can collide on shared floating tags. The `IMAGE_TAG=pr-<N>-<sha>` refactor solved this; if you see `Conflicted with another upload of the same manifest`, `gh run rerun --failed`.

## Cleanup after release

- The `release-X.Y.Z` branch is deleted by GitHub when its PR merges. The `release-X.Y` maintenance branch is created by `cut-prerelease.yaml` at the rc freeze (create-only, never force-moved) and fast-forwarded afterwards by `pull-requests-release.yaml::Ensure maintenance branch`.
- The draft release is published by the same workflow.
- The release body is set by finalize from `docs/changelogs/vX.Y.Z.md` at publication. Afterwards, `update-releasenotes.yaml` re-syncs that file into the GitHub Release body on any push to `main` that touches it — so edits to a published changelog land on the release page the next time `main` moves.
- Local cleanup: remove your worktree (`git worktree remove`) and prune merged release branches.

## See also

- [`agents/changelog.md`](./agents/changelog.md) — canonical changelog generation process.
- [`agents/contributing.md`](./agents/contributing.md) — commit/PR conventions, backport label semantics.
- [`agents/releasing.md`](./agents/releasing.md) — pointer file for AI agents handling release tasks.
- [`.github/workflows/cut-prerelease.yaml`](../.github/workflows/cut-prerelease.yaml) — the sole entry point for cutting a pre-release tag as the CI app.
- [`.github/workflows/tags.yaml`](../.github/workflows/tags.yaml) — tag-push pipeline (rc build + mandatory `rc-e2e`; stable-tag changelog/website backstops).
- [`.github/workflows/e2e-tag.yaml`](../.github/workflows/e2e-tag.yaml) — mandatory rc and manual published-tag full E2E lane.
- [`.github/workflows/pull-requests-release.yaml`](../.github/workflows/pull-requests-release.yaml) — merge-finalize pipeline.
- [`.github/workflows/promote-rc.yaml`](../.github/workflows/promote-rc.yaml) — rc → stable promotion.
- [`.github/workflows/changelog-rc.yaml`](../.github/workflows/changelog-rc.yaml) — optional rc-time changelog button.
- [`.github/workflows/changelog-generate.yaml`](../.github/workflows/changelog-generate.yaml) — reusable changelog-generation core shared by promote-rc and changelog-rc.
- [`.github/workflows/nightly.yaml`](../.github/workflows/nightly.yaml) — nightly: mirror `main` OCIR→GHCR + full e2e.
- [`hack/nightly-mirror.sh`](../hack/nightly-mirror.sh) — cross-registry image mirror used by the nightly.
- [`agents/image-refs.md`](./agents/image-refs.md) — where image references live, what their tags mean, and the invariants promotion and mirroring rely on.
- [`hack/lib/image-refs.sh`](../hack/lib/image-refs.sh) — the single enumeration of ref-bearing files, shared by the promote, retag and mirror tooling.
- [`.github/workflows/retention.yaml`](../.github/workflows/retention.yaml) — GHCR nightly pruning.
- [`.github/workflows/backport.yaml`](../.github/workflows/backport.yaml) — automatic cherry-pick bot.
- [`.github/workflows/update-releasenotes.yaml`](../.github/workflows/update-releasenotes.yaml) — sync changelog → GitHub Release body.
