## Cleanup after release

- The `release-X.Y.Z` branch is deleted by GitHub when its PR merges. The `release-X.Y` maintenance branch is created by `cut-prerelease.yaml` at the rc freeze (create-only, never force-moved) and fast-forwarded afterwards by `pull-requests-release.yaml::Ensure maintenance branch`.
- The draft release is published by the same workflow.
- The release body is set by finalize from `docs/changelogs/vX.Y.Z.md` at publication. Afterwards, `update-releasenotes.yaml` re-syncs that file into the GitHub Release body on any push to `main` that touches it — so edits to a published changelog land on the release page the next time `main` moves.
- Local cleanup: remove your worktree (`git worktree remove`) and prune merged release branches.
