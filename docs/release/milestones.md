## Milestones

A milestone titled exactly as the release tag, such as `v1.7.0` or `v1.6.6`, is the scope of that release, and the end of the milestone is the release. What the milestone holds reaches users when `vX.Y.Z` is published, and not before.

- Maintainers put an issue or PR in the milestone once they commit to shipping it in that release. After the line is frozen, work merged to `main` reaches the release only by [backport](./backports.md#backports). For a frozen line, the milestone therefore holds what must be on `release-X.Y` before promotion.
- The due date is optional. Set it when the release has a target date. On that day the release starts with whatever is still open.

[`milestone-release.yaml`](../../.github/workflows/milestone-release.yaml) starts the release once the milestone is done, meaning its last item has closed or its due date has come. It checks when an issue in a milestone closes and once a day. It opens one `Release vX.Y.Z` issue inside the milestone, labelled `area/release` and `triage/accepted`. The issue lists:

- what was delivered: merged PRs and issues closed as completed;
- anything still open, to be finished or moved to the next milestone before the rc is cut;
- the steps from [Regular Releases](./regular-releases.md#regular-releases), with the exact rc tag and the branch to dispatch it from.

The workflow never cuts a tag, creates a branch or dispatches another workflow. Cutting the rc freezes the line, so it stays a maintainer's dispatch, and the promote PR still needs a reviewed merge. The workflow only removes the need for someone to notice that the milestone is done. After the stable `vX.Y.Z` release is published, its next run closes the issue and then the milestone. A milestone whose title is not `vX.Y.Z` is ignored. A manual dispatch is a dry run unless `dry_run` is unchecked.
