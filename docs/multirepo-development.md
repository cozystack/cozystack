# Multirepo Development (cozystack + cozystack-ui)

Cozystack spans two repositories that ship as one product:

| Repo | Role |
|------|------|
| [`cozystack/cozystack`](https://github.com/cozystack/cozystack) | monorepo — Helm charts, Go controllers, CRDs, the release pipeline |
| [`cozystack/cozystack-ui`](https://github.com/cozystack/cozystack-ui) | the dashboard frontend, built into the dashboard image |

Both repos use the same branching model — `main` for development, `release-X.Y` for maintenance — and the two `release-X.Y` lines are kept in lockstep so a patch release never ships unreleased UI work. This guide explains how the two repos move together. For the release pipeline itself, see [`release.md`](./release.md).

## The seam: how the UI gets into a build

The dashboard image is built by `packages/system/dashboard/Makefile`, which clones **one** branch of `cozystack-ui` at build time, selected by `CONSOLE_BRANCH`. That single variable is what ties a cozystack release line to a cozystack-ui release line.

`CONSOLE_BRANCH` is resolved automatically — there is no per-branch value to maintain by hand:

| Build context | How it's resolved | Result |
|---------------|-------------------|--------|
| `cozystack/main`, feature branches (CI or local) | Makefile derives from git branch | `cozystack-ui@main` |
| `cozystack/release-X.Y` checkout (local) | Makefile derives from git branch | `cozystack-ui@release-X.Y` |
| Release build (`tags.yaml`, detached HEAD on a tag) | workflow passes the tag's base branch explicitly | `main` for `vX.Y.0`, `release-X.Y` for patch tags |

The release build must supply the value explicitly because a tag checkout is a detached `HEAD` where the branch name is unavailable. Local builds derive it from the checked-out branch, so a `release-X.Y` checkout always builds the matching UI without any extra flags.

## Developing a feature

Everything new lands on `main` first — in **both** repos, always.

- **Backend / chart / CRD change** → PR to `cozystack/main`.
- **UI change** → PR to `cozystack-ui/main`. The repo's `test` job (type-check + build) gates the PR.

Because a `cozystack/main` build clones `cozystack-ui@main`, the development branch always integrates the latest UI. No coordination commit is needed between the repos for day-to-day feature work.

## Cutting a new minor (`vX.Y.0`)

This is automatic. When the regular-release PR merges, `pull-requests-release.yaml` finalize:

1. Creates `cozystack/release-X.Y` (the cozystack maintenance branch).
2. Creates `cozystack-ui/release-X.Y` from `cozystack-ui/main` HEAD.

The new `cozystack/release-X.Y` inherits the branch-deriving Makefile, so it is wired to `cozystack-ui/release-X.Y` from the moment it exists. Nothing to pin, nothing to remember.

> Prerequisite: the `cozystack-ci` GitHub App must be installed on `cozystack-ui` with `contents:write`, so finalize can create the branch cross-repo.

## Backporting a fix to a patch release (`vX.Y.Z`)

This is the one step that needs human judgment, and it is symmetric across both repos — the same discipline as any backend backport:

- **Backend fix**: land on `cozystack/main` → cherry-pick to `cozystack/release-X.Y`.
- **UI fix**: land on `cozystack-ui/main` → cherry-pick to `cozystack-ui/release-X.Y`.

`auto-release.yaml` (nightly) notices commits ahead of the latest tag on `cozystack/release-X.Y`, cuts `vX.Y.Z`, and the build clones `cozystack-ui@release-X.Y` automatically. A UI-only fix therefore needs **no** cozystack-side commit — the next patch build picks it up on its own.

Features are never cherry-picked. Only fixes to functionality that already shipped in `vX.Y.0` belong on a release branch.

## Mental model

A change is "in" a release line only when it has been cherry-picked to that line in **both** repos. Everything else lives on `main`.

The minor cut and the build wiring are automatic. The cherry-pick triage — deciding what is a safe backport versus an unreleased feature — is the human's job, identical on both sides of the seam.

## See also

- [`release.md`](./release.md) — the full release pipeline, including the "UI release branch (dashboard)" section.
- [`agents/contributing.md`](./agents/contributing.md) — commit/PR conventions and backport label semantics.
- [`../packages/system/dashboard/Makefile`](../packages/system/dashboard/Makefile) — `CONSOLE_BRANCH` resolution.
