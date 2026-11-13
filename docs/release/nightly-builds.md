## Nightly builds

A nightly is an **installable copy of `main` on GHCR — not a rebuild and not a release**. [`build-main.yaml`](../../.github/workflows/build-main.yaml) already builds every push to `main` into the CI registry (OCIR); the nightly promotes that build to the public release registry (GHCR) and proves it installs.

[`nightly.yaml`](../../.github/workflows/nightly.yaml) runs daily (gated by the `NIGHTLY_ENABLED` repo variable) as three jobs; build-disk and e2e both start once mirror has finished:

1. **mirror** — resolve the exact commit `cozystack-packages:main` was built from, then [`hack/nightly-mirror.sh`](../../hack/nightly-mirror.sh) copies every cozystack-owned component image OCIR→GHCR **by digest** (bit-for-bit, no rebuild) and re-publishes the rewritten `cozystack-packages` artifact and the `cozy-installer` chart to GHCR, tagged `0.0.0-nightly.<YYYYMMDD>.<run-id>.<attempt>` plus a floating `nightly`.
2. **build-disk** — assemble the Talos `nocloud` disk from the upstream siderolabs imager (the profile references only `ghcr.io/siderolabs/*`, so this needs no cozystack image and no rebuild) and publish it as a GHCR OCI artifact (`cozystack-nocloud`) so a nightly is installable on real hardware. The nightly e2e does not boot it.
3. **e2e** — stage the published GHCR closure (rewritten tree + the `cozy-installer` chart pinned to the GHCR packages artifact) and run the **full** app suite on Talos containers, which do not boot the disk. A nightly has no diff, so Test Impact Analysis does not apply.

No GitHub release and no `api/apps/v1alpha1/*` Go-module tag are created for a nightly. [`retention.yaml`](../../.github/workflows/retention.yaml) prunes old GHCR nightly versions (keeps the newest 14 per package the current nightly publishes, named from the published `cozystack-packages:nightly` tree by [`hack/retention-packages.sh`](../../hack/retention-packages.sh), except packages linked to another repository, which are skipped with a warning; the floating `nightly`, release tags and untagged versions are never touched) and abandoned packages candidates. A candidate version is eligible only when all of these hold: it carries at least one tag and **every** tag on that manifest matches the strict `promotion-vX.Y.Z-from-vX.Y.Z-rc.N-run-<run-id>-<attempt>` form, so a manifest that has been retagged stable is excluded; it was last updated more than 30 days ago; its digest is not pinned by `packages/core/installer/values.yaml` at the head of an **open** pull request that is same-repository, authored by `cozystack-ci[bot]`, on a `release-X.Y.Z` branch and carrying the `release` label; and its digest is not pinned by that same file at the tip of `main` or of any `release-X.Y` branch — which is what keeps a candidate safe between the promote PR merging and finalize retagging it. Any such PR head whose pin is not exactly one `digest=sha256:<64 hex>` stops the candidate sweep for that run and fails the job, without affecting the nightly sweep.

Install a nightly:

```bash
helm upgrade --install cozystack \
  oci://ghcr.io/cozystack/cozystack/cozy-installer --version 0.0.0-nightly.20260626.15992304129.1
# ...or --version nightly for the latest
```

The matching Talos node image is `ghcr.io/cozystack/cozystack/cozystack-nocloud:0.0.0-nightly.20260626.15992304129.1` (pull with `oras`).
