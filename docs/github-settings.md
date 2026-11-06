# GitHub settings

This page describes the GitHub settings that guard `cozystack/cozystack`: what is configured, who may bypass it, and how a change is made. It is a description, not a source of truth. GitHub holds the live settings, and nothing applies this page or the exported files back.

## Rulesets

[`.github/rulesets/`](../.github/rulesets) holds a JSON export of every ruleset that applies to this repository, written by [`hack/export-rulesets.sh`](../hack/export-rulesets.sh). Files under `repo/` belong to this repository. Files under `org/` belong to the `cozystack` organization and apply here along with its other repositories; the export has their rules and ref conditions but not the list of repositories they target, which is set in the organization settings.

`cozystack-ci` below is the GitHub App the release and promotion workflows run as. An organization admin can bypass only where the table says so.

| Ruleset | Applies to | Rules | Bypass |
|---|---|---|---|
| Require DCO on default branches (org) | default branch | `DCO` required status check, not enforced when the branch is created | organization admins, `cozystack-ci` |
| Require PR review for default branches (org) | default branch | pull request required, 1 approval, stale approvals dismissed on push | none |
| Protect release lines (release-X.Y) | `release-*` except `release-*.*.*` | no deletion, no force push, no creation; pull request required, 1 approval, stale approvals dismissed on push, merge commits only | `cozystack-ci` |
| Protect main | `main` | no deletion, no force push; pull request required, 1 approval from a code owner, stale approvals dismissed on push; required checks `pre-commit` and `E2E Tests` and `API Review Gate` (all from GitHub Actions) and `CodeQL` (code scanning), the branch need not be up to date | none |
| Release tags are created by CI only | tags `v*` and `api/apps/v1alpha1/v*` | creation restricted | `cozystack-ci` |
| Release tags are immutable | tags `v*` and `api/apps/v1alpha1/v*` | no update, no deletion, no force push | none |

The two tag rulesets back the release workflow described in [release.md](release.md#repo-side-enforcement-tag-protection-rulesets).

## Required checks on main

GitHub applies the most restrictive result of all rules that match a branch, so `main` is held to the two organization rulesets and to Protect main together. Protect main has no bypass actors, so the rules apply to administrators as well.

`E2E Tests` and `API Review Gate` are commit statuses, not check runs, and every workflow that posts them does so with the default `GITHUB_TOKEN`, which is why each is pinned to the GitHub Actions app. A lane that posted one of them with another token would have to be moved to the default token first, because a ruleset pins one source per context. Why they are matched by status context and not by job name is explained in [`docs/agents/e2e-testing.md`](agents/e2e-testing.md) and in the header of [`api-review-gate.yaml`](../.github/workflows/api-review-gate.yaml).

## Repository settings

- Merge commits only; squash and rebase merging are off.
- Auto-merge is on, and a branch is deleted after its pull request merges.
- Wiki is off, Discussions is on.
- Secret scanning and push protection are on.

## Changing a setting

A change goes through a pull request so that the reason is on record and a second maintainer sees it.

1. Open a pull request that edits this page and, for a ruleset, the matching file under `.github/rulesets/`. Reviewers are the maintainers in [`CODEOWNERS`](../.github/CODEOWNERS).
2. After approval, an organization admin applies the change under Settings → Rules (a new ruleset can be created with Import a ruleset from the JSON file).
3. The admin runs `hack/export-rulesets.sh` and commits any difference, which should be formatting only. A difference that is not formatting means the live setting is not the one that was approved.

A change made in the GitHub UI first, for example during an incident, is followed the same day by a pull request that brings this page and the export in line.

The export needs a token with admin access to the repository, because GitHub returns the bypass list to admins only. Without it the script refuses to write anything, so a partial export cannot overwrite a complete one.
