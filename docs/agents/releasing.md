# Release Process

This document provides instructions for AI agents on how to handle release-related tasks.

## When to Use

Follow these instructions when the user asks to:
- Create a new release
- Prepare a release
- Tag a release
- Perform release-related tasks

## Instructions

For detailed release process instructions, follow the steps documented in the pages under `docs/release/`, starting from their index:

**[docs/release.md](../release.md)**

## Quick Reference

The release process is promotion-based — a stable release is a renamed release-candidate, never a hand-cut tag or a rebuild. At a high level it involves:
1. Cutting a release candidate (`Cut Pre-release Tag` workflow) and watching its mandatory full E2E.
2. Promoting the rc (`Promote RC` workflow), which generates the changelog, opens the parked `cozystack/website` docs PR, and opens the promote PR.
3. Reviewing and merging the promote PR (no squash-merge).
4. Finalize on merge (CI cuts the write-once tag, publishes the release from the merged changelog, and retags the rc images to stable by digest).
5. Merging the parked website docs PR once the release is published.

All detailed steps, workflows, and failure modes are documented in the pages under `docs/release/`, one per topic, and `docs/release.md` links each of them.

What a patch release carries is decided by the rules in [`docs/release/backports.md`](../release/backports.md), not per pull request: fixes reach the release lines through the backport batch, and nobody labels or opens a backport. For a patch release, or any rc cut from a `release-X.Y` branch, check the branch before tagging: build the batch tool with `go build ./cmd/backport-batch`, then run `./backport-batch check --line release-X.Y`. It exits 1 while a pull request the rules take is neither on the line nor on the open batch pull request, and prints the URLs; the conflicts the batch deferred print as warnings. See [`cmd/backport-batch/README.md`](../../cmd/backport-batch/README.md) and [Cherry-pick triage before a patch](../release/backport-mechanics.md#cherry-pick-triage-before-a-patch). Do not report a branch as ready to cut on the strength of a list of merged pull requests or of the batch pull request's description — they list what merged or was picked, not what arrived.

