# Release Process

This document provides instructions for AI agents on how to handle release-related tasks.

## When to Use

Follow these instructions when the user asks to:
- Create a new release
- Prepare a release
- Tag a release
- Perform release-related tasks

## Instructions

For detailed release process instructions, follow the steps documented in:

**[docs/release.md](../release.md)**

## Quick Reference

The release process is promotion-based — a stable release is a renamed release-candidate, never a hand-cut tag or a rebuild. At a high level it involves:
1. Cutting a release candidate (`Cut Pre-release Tag` workflow) and watching its mandatory full E2E.
2. Promoting the rc (`Promote RC` workflow), which generates the changelog, opens the parked `cozystack/website` docs PR, and opens the promote PR.
3. Reviewing and merging the promote PR (no squash-merge).
4. Finalize on merge (CI cuts the write-once tag, publishes the release from the merged changelog, and retags the rc images to stable by digest).
5. Merging the parked website docs PR once the release is published.

All detailed steps, workflows, and failure modes are documented in `docs/release.md`.

