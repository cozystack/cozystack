## Backports

Backports are automatic. Everything that can go into a patch release goes in, and nobody decides it pull request by pull request: nobody requests a backport, nobody labels for one, and contributors never open backport pull requests. A batch job takes the fixes merged into `main` and cherry-picks them onto each supported `release-X.Y` line, following the rules on this page.

### Getting a fix into a patch release

Merge it into `main` with a `fix` title, for example `fix(postgres): keep the replica count on resize`. Nothing else is needed: if the change meets the rules below, the next batch for each supported line picks it up.

The batch takes a pull request's type from its title alone, so a fix that ships inside a `feat` or `refactor` pull request does not reach a patch release. Put the fix in a pull request of its own.

### What the batch takes

The batch takes a pull request from `main` into release line X.Y when all of these hold:

1. It merged into `main` after `release-X.Y` branched, and the line does not carry it yet.
2. Its title type is `fix` or `security`. CI and e2e fixes count, because they keep the line's own CI working.
3. It is not breaking: no `!` after the type or scope, and no `BREAKING CHANGE` footer.
4. It touches no platform migrations, no `targetVersion`, nothing under `api/` and no CRDs. Those wait for the next minor. [Backporting code that touches monotonic counters](./backport-mechanics.md#backporting-code-that-touches-monotonic-counters) shows what a migration on a release line breaks.
5. It picks cleanly with `git cherry-pick -x`, or its conflict is resolved during the batch and the batch's build and tests pass. A pick that fails moves to the next batch, and the batch report gives the reason.
6. On an older line, it is already on the next newer line. Lines are processed newest first, so an older line takes only what the newer one already carries.

### What never goes into a patch release

`feat`, `chore` and `docs` pull requests, and dependency bumps (scope `deps`), never go into a patch release. They ship with the next minor. If a class of fixes should also wait for a minor, it gets a new rule on this page; nobody decides it per pull request.

### Fixes that exist only on a release line

A fix that only makes sense on a release line, with no counterpart on `main`, gets the `release-only` label from a maintainer. Only then may a pull request from outside the team stay open against a release branch.

A pull request opened or reopened against a `release-*` branch is closed automatically, with a comment that links to this page, only when its author is not a bot, GitHub reports the author's access to the repository as read or none, and it carries no `release-only` label. Anyone with triage access or more keeps their pull request open, and so does everyone when GitHub's answer cannot be read. To keep one that was closed, a maintainer adds the label first and then reopens it; reopening without the label closes it again. [`hack/close-manual-backport.sh`](../../hack/close-manual-backport.sh) does this from [`pr-labeler.yaml`](../../.github/workflows/pr-labeler.yaml); run it with `--pr <number> --dry-run` to see what it would decide for a given pull request.

### Cadence

The batch runs for each supported release line twice a week, on Monday and Thursday. Patch releases ship weekly, and minor releases every four weeks.

### Running the batch

Lands with the batch tool.

### The label-driven bot

[`backport.yaml`](../../.github/workflows/backport.yaml) still cherry-picks a merged pull request that carries `kind/backport` or `kind/backport-previous`. It is no longer how a backport is requested, but maintainers may still use it as a manual fallback for a single pull request. [Backport mechanics](./backport-mechanics.md) describes how it works and what to do when it fails.
