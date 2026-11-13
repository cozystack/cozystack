## Introduction

Cozystack uses a staged release process to ensure stability and flexibility during development.

There are three types of releases:

- **Release Candidates (RC)** – Preview versions (e.g., `v1.2.0-rc.1`) used for final testing and validation.
- **Regular Releases** – Final versions (e.g., `v1.2.0`) that are feature-complete and thoroughly tested.
- **Patch Releases** – Bugfix-only updates (e.g., `v1.2.1`) made after a stable release, based on a dedicated release branch.

All three are matched by `tags.yaml`'s regex `^v\d+\.\d+\.\d+(-(alpha|beta|rc)\.\d+)?$`. The tag push is the trigger for the whole pipeline. **Pre-release tags (`-alpha` / `-beta` / `-rc`) are cut with the [`Cut Pre-release Tag`](../../.github/workflows/cut-prerelease.yaml) workflow** (manual dispatch from `main` or `release-X.Y`) — it pushes the tag as the CI app at the dispatch branch's tip, so GitHub fills `base_ref` (`tags.yaml`'s `Get base branch` step refuses tags pushed without a base) and the push triggers `tags.yaml` (a tag pushed with the default `GITHUB_TOKEN` would not). Stable `vX.Y.Z` tags are never pushed by hand at all — they are cut write-once by the promote flow at a PR merge commit. Only `-rc.N` pre-releases feed the promotion flow ([`promote-rc.yaml`](../../.github/workflows/promote-rc.yaml) accepts `vX.Y.Z-rc.N` alone); `-alpha.N` / `-beta.N` tags are built and published for download and validated on demand with the [`e2e-tag.yaml`](../../.github/workflows/e2e-tag.yaml) button, but are never promoted to a stable release.
