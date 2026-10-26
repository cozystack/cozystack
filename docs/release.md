# Release Workflow

The release documentation is both the process design (what the release model looks like) and the operational playbook (what to actually do, what breaks, what to verify), split into one page per topic under [`release/`](./release/). Companion to [`agents/changelog.md`](./agents/changelog.md), which is the canonical changelog process.

## When to use this guide

You are about to:

- Cut a pre-release (`vX.Y.0-rc.N`, or `vX.Y.0-alpha.N` / `-beta.N`).
- Cut a regular release (`vX.Y.0`).
- Cut a patch release (`vX.Y.Z` with `Z > 0`).
- Triage cherry-picks before a patch release.
- Investigate why CI is misbehaving on a tag push.
- Review a promote (release) PR.

If you are about to generate a changelog only, jump straight to [`agents/changelog.md`](./agents/changelog.md).

## Pages

- [Introduction](./release/introduction.md): the three release types and the tag push that drives the pipeline.
- [Release Candidates](./release/release-candidates.md): what an rc is, and how cutting the first one freezes `release-X.Y`.
- [Regular Releases](./release/regular-releases.md): promoting an rc to `vX.Y.0`, step by step.
- [Patch Releases](./release/patch-releases.md): the same flow for `vX.Y.Z` on an existing `release-X.Y` line.
- [What CI does during the release process](./release/ci-pipeline.md): which workflow does what, phase by phase, including multi-arch builds and the rc E2E gate.
- [Stable tags come from rc promotion](./release/stable-tags.md): why a stable tag only ever comes from promoting an rc.
- [Release signatures and SBOM](./release/signatures-and-sbom.md): the SBOM, checksums and cosign signature on each release, and how to verify them.
- [Nightly builds](./release/nightly-builds.md): how a nightly mirrors `main` to GHCR, and how to install one.
- [Backports](./release/backports.md): the policy that decides what goes into a patch release, and how a contributor gets a fix there.
- [Backport mechanics](./release/backport-mechanics.md): cherry-pick triage before a patch, and the monotonic-counter trap.
- [Backport tasks](./release/backport-tasks/README.md): resolving a batch conflict, deferring the original a failed batch CI comes from, and checking a pull request title, with the check each has to pass.
- [Changelogs](./release/changelogs.md): changelog failure modes and the pre-publish checks.
- [Release-time fires](./release/release-time-fires.md): regressions that escaped to users, and the verification gaps they expose.
- [Pre-release verification checklist](./release/pre-release-checklist.md): what to verify before merging a release PR.
- [CI failures release engineers commonly hit](./release/ci-failures.md): known failures and whether each one blocks a release.
- [Tag immutability](./release/tag-immutability.md): write-once tags and the rulesets that enforce them.
- [Splitting a release-blocking bundle PR](./release/splitting-bundle-prs.md): how to break up a grab-bag PR that blocks a release.
- [Cleanup after release](./release/cleanup.md): what is left to clean up once a release is out.

## See also

- [`agents/changelog.md`](./agents/changelog.md) — canonical changelog generation process.
- [`agents/contributing.md`](./agents/contributing.md) — commit/PR conventions.
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
- [`.github/workflows/retention.yaml`](../.github/workflows/retention.yaml) — GHCR nightly and abandoned promotion-candidate pruning.
- [`cmd/backport-batch`](../cmd/backport-batch/README.md) — the backport batch that applies the [backport policy](./release/backports.md).
- [`.github/workflows/backport-batch.yaml`](../.github/workflows/backport-batch.yaml) — runs the backport batch for each supported line on Monday and Thursday.
- [`.github/workflows/update-releasenotes.yaml`](../.github/workflows/update-releasenotes.yaml) — sync changelog → GitHub Release body.
