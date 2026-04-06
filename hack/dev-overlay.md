# Dev Overlay

Patch the cluster's platform OCI artifact with your local changes without rebuilding or uploading the entire packages tree.

## Why not `make image-packages`?

`make image-packages` (from `packages/core/installer/Makefile`) pushes the **entire** `packages/` directory from your current working tree as a new OCI artifact. This has two problems:

1. **Image version leakage.** Your working tree inherits image versions from the branch you're on (e.g. `cozystack-controller:v1.1.0` on main), which differ from the pinned versions in the cluster's release (e.g. `v1.1.4`). Uploading the whole tree overwrites them all.

2. **Single-branch only.** If you develop features across multiple branches (branch A changes component A, branch B changes component B), you can only upload from one branch at a time. The old workaround was to create a "frankenstein" branch by merging all feature branches onto a release tag — manual and error-prone.

## How dev-overlay works

Instead of replacing the entire artifact, dev-overlay **patches** it with only the files you changed:

1. Pulls the existing overlay from the registry (or the cluster's base OCI on first run)
2. Runs `git diff origin/main -- packages/` against your working tree to find changed files
3. Copies only those files into the artifact, deletes removed files, cleans up renames
4. Pushes the patched artifact and points the operator at its digest

Because the diff is against `origin/main` (or a ref you choose), files you didn't touch — including values with pinned image versions — are never overwritten. The cluster keeps its release versions.

## Accumulation across branches

Changes from multiple branches stack on top of each other:

```
# On branch A (changes component A)
make dev-overlay

# Switch to branch B (changes component B)
make dev-overlay
```

After both runs, the overlay contains changes from both A and B. Files that only A touched are preserved when B runs (B's diff doesn't mention them). If both branches modify the same file, the last applied version wins.

## Usage

```bash
cd packages/core/installer

# Preview what would be applied
make dev-overlay-diff

# Apply changes (defaults to diffing against origin/main)
make dev-overlay

# Override the diff base
make dev-overlay DEV_BASE_TAG=some-commit-hash
```

## Resetting

To discard all overlay changes, delete the overlay tag from the registry and restore the operator to the original platform source. There is no automated undo — just point the operator back at the base artifact.

## How DEV_BASE_TAG affects correctness

`DEV_BASE_TAG` (default: `origin/main`) is the git ref your changes are diffed against. For image versions to be preserved correctly, it must be a ref where inherited values match your working tree:

- Your branch has `image: v1.1.0` (inherited from main)
- `origin/main` also has `image: v1.1.0`
- No diff detected — cluster keeps its `v1.1.4`

If you rebase onto a newer main that bumped a version, `origin/main` tracks that automatically. If you need a fixed point, pass a specific commit hash.
