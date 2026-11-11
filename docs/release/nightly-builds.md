## Nightly builds

A nightly is an **installable copy of `main` on GHCR — not a rebuild and not a release**. [`build-main.yaml`](../../.github/workflows/build-main.yaml) already builds every push to `main` into the CI registry (OCIR); the nightly promotes that build to the public release registry (GHCR) and proves it installs.

[`nightly.yaml`](../../.github/workflows/nightly.yaml) runs daily (gated by the `NIGHTLY_ENABLED` repo variable) as three jobs; build-disk and e2e both start once mirror has finished:

1. **mirror** — resolve the exact commit `cozystack-packages:main` was built from, then [`hack/nightly-mirror.sh`](../../hack/nightly-mirror.sh) copies every cozystack-owned component image OCIR→GHCR **by digest** (bit-for-bit, no rebuild) and re-publishes the rewritten `cozystack-packages` artifact and the `cozy-installer` chart to GHCR, tagged `0.0.0-nightly.<YYYYMMDD>.<run-id>.<attempt>` plus a floating `nightly`.
2. **build-disk** — assemble the Talos `nocloud` disk from the upstream siderolabs imager (the profile references only `ghcr.io/siderolabs/*`, so this needs no cozystack image and no rebuild) and publish it as a GHCR OCI artifact (`cozystack-nocloud`) so a nightly is installable on real hardware. The nightly e2e does not boot it.
3. **e2e** — stage the published GHCR closure (rewritten tree + the `cozy-installer` chart pinned to the GHCR packages artifact) and run the **full** app suite on Talos containers, which do not boot the disk. A nightly has no diff, so Test Impact Analysis does not apply.

No GitHub release and no `api/apps/v1alpha1/*` Go-module tag are created for a nightly. [`retention.yaml`](../../.github/workflows/retention.yaml) prunes old GHCR nightly versions (keeps the newest 14 per package the current nightly publishes, named from the published `cozystack-packages:nightly` tree by [`hack/retention-packages.sh`](../../hack/retention-packages.sh), except packages linked to another repository, which are skipped with a warning; the floating `nightly`, release tags and untagged versions are never touched) and abandoned packages candidates. A candidate version is eligible only when all of these hold: it carries at least one tag and **every** tag on that manifest matches the strict `promotion-vX.Y.Z-from-vX.Y.Z-rc.N-run-<run-id>-<attempt>` form, so a manifest that has been retagged stable is excluded; it was last updated more than 30 days ago; its digest is not pinned by `packages/core/installer/values.yaml` at the head of an **open** pull request that is same-repository, authored by `cozystack-ci[bot]`, on a `release-X.Y.Z` branch and carrying the `release` label; and its digest is not pinned by that same file at the tip of `main` or of any `release-X.Y` branch — which is what keeps a candidate safe between the promote PR merging and finalize retagging it. Any such PR head whose pin is not exactly one `digest=sha256:<64 hex>` stops the candidate sweep for that run and fails the job, without affecting the nightly sweep.

### Release-line baseline

The same workflow tests every release line the [backport batch](./backports.md) runs for, on a cron of its own at 22:17 UTC so that it ends before the batch at 06:23. Its runs are titled **Nightly release-line baseline**. It is gated by the same `NIGHTLY_ENABLED` variable; a dispatch with `release_baseline` ticked runs it on demand. The baseline's cron and its dispatch run none of main's jobs and republish nothing of main's, and main's cron and a plain dispatch run none of the baseline's; each half has its own concurrency group, so main's nightly never waits behind the baseline. The logic is [`hack/release-baseline.sh`](../../hack/release-baseline.sh), and every command of it runs by hand from a worktree, for example `hack/release-baseline.sh discover`.

1. **discover** — `backport-batch lines` names the lines, so a line in its rc window joins and a retired one leaves without an edit anywhere. Each line's head on `origin` is pinned here, and every later job tests that commit, never the branch. It also reads `cozystack-packages:<line>` from the CI registry: the artifact is fresh when it was built from the pinned commit, or from an ancestor whose changes all lie in the paths [`build-release.yaml`](../../.github/workflows/build-release.yaml) ignores on push. No line, or a malformed one, fails the run.
2. **rebuild** — a missing or stale artifact is rebuilt by calling `build-release.yaml` for the line at the pinned commit. It publishes the artifact exactly as a push to the line would, so the line's pull requests get their base images back too. It waits for a push build of the same line rather than cancel it, a push still cancels it, and it refuses to publish once the line has moved past the pinned commit; that line is then red for the night, and its push build has published the newer head.
3. **unit** — the line's own unit job: the `make` commands and the tool pins of its `pull-requests.yaml`, read from the line's tree. A line that installs helm-unittest unpinned gets the version `main` pins instead, because no workflow on `main` may install it unpinned; on such a line a helm unit test can disagree with the line's own pull requests.
4. **e2e-line** — the full app suite through the line's own harness, on the line's artifact pinned by digest: the digest the rebuild produced, or the fresh one discover found, refused unless it was built from the pinned commit. The ceiling is the 215 minutes every full-suite lane is held to. A red unit leg stops neither this job nor another line.
5. **report** — one summary per line: the pinned SHA, the artifact digest and whether it was rebuilt, the unit and e2e results, and the run. A line with a missing result, or an e2e with no test case, is red, and so is the run.

A red baseline run means at least one line is red at the SHA its summary names; the run is the whole report, and nothing is posted anywhere or written to any pull request. To read a red batch pull request against it, take the last baseline run before the batch's CI and compare the SHA it pinned for the line with the batch's base. The same failure in the baseline at that SHA means the line was red already. A green baseline there means the batch brought the failure, or it is a flake. A baseline at an older SHA than the batch's base says nothing about the commits in between, and the full suite covers only what the container lane can run.

Find the runs with:

```bash
gh run list --workflow nightly.yaml --json displayTitle,createdAt,conclusion,url \
  --jq '.[] | select(.displayTitle == "Nightly release-line baseline")'
```

Install a nightly:

```bash
helm upgrade --install cozystack \
  oci://ghcr.io/cozystack/cozystack/cozy-installer --version 0.0.0-nightly.20260626.15992304129.1
# ...or --version nightly for the latest
```

The matching Talos node image is `ghcr.io/cozystack/cozystack/cozystack-nocloud:0.0.0-nightly.20260626.15992304129.1` (pull with `oras`).
