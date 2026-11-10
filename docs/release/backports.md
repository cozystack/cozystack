## Backports

Backports are automatic. Everything that can go into a patch release goes in, and nobody decides it pull request by pull request: nobody requests a backport, and contributors never open backport pull requests. The one exception is a maintainer override, below. A batch job takes the fixes merged into `main` and cherry-picks them onto each `release-X.Y` line it runs for, following the rules on this page.

### Getting a fix into a patch release

Merge it into `main` with a `fix` title, for example `fix(postgres): keep the replica count on resize`. Nothing else is needed: if the change meets the rules below, the next batch picks it up for every [line the batch runs for](#which-lines-the-batch-runs-for).

The batch takes a pull request's type from its title alone, so a fix that ships inside a `feat` or `refactor` pull request does not reach a patch release. Put the fix in a pull request of its own.

### What the batch takes

The batch takes a pull request from `main` into release line X.Y when all of these hold:

1. It merged into `main` after `release-X.Y` branched, or after the line started under this policy if that is later (see [Where each line starts](#where-each-line-starts)), and the line does not carry it yet.
2. Its title type is `fix` or `security`, the type of a fix that comes through a security advisory. CI and e2e fixes count, because they keep the line's own CI working. The title is read once any leading `[Backport …]` prefixes are dropped, and a title that does not read as a Conventional Commits title is kept out.
3. It is not breaking: no `!` after the type or scope, and no `BREAKING CHANGE:` or `BREAKING-CHANGE:` footer at the start of any line of the description its merge into `main` recorded. A footer inside an HTML comment, where the pull request template quotes it, does not count.
4. It touches no platform migrations, anything under `packages/core/platform/images/migrations/migrations/`, and no `targetVersion` line of `packages/core/platform/values.yaml`, adds, removes or edits no CustomResourceDefinition document (a CRD in a test fixture under `tests/` or `testdata/` does not count, and neither does an edit to another document in the same file), and makes no change to the API that the [API review gate](../../.github/workflows/api-review-gate.yaml) calls sizeable: a new or removed API group or resource, or a schema change that can reject what the old schema accepted. Those wait for the next minor; a reworded description or a new optional field under `api/` does not. [Backporting code that touches monotonic counters](./backport-mechanics.md#backporting-code-that-touches-monotonic-counters) shows what a migration on a release line breaks.
5. Each of its commits picks cleanly with `git cherry-pick -x`, or its conflict is resolved during the batch, and the batch's build and tests pass. A pick that fails moves to the next batch, and the batch pull request lists it with the files that conflicted.
6. On an older line, it is already on the next newer line the batch runs for. Lines are processed newest first, so an older line takes only what the newer one already carries.

The batch reads the pull requests off `main`'s first-parent history, one per merge commit or squash commit that names its pull request. A commit there that names none, a direct push for instance, and a merge of more than two parents are never taken; the batch pull request counts them.

### What the batch leaves out on one line

A pull request the rules take for every line can still be wrong for one of them, for instance because it builds on code that line does not have. A maintainer drops it from that line with `./backport-batch run --line <line> --drop <number> --reason <why> --push`: the batch takes it off, if it carries it, and no later batch for that line picks it again. The drop is kept in the [line's record](#the-lines-record-and-reports), so it needs an open batch pull request to record it on; before a line's first batch pull request exists there is nothing to record it on, and the command refuses. `--undrop <number>` withdraws it, and the next batch tries the pull request again. A pull request the line already carries cannot be dropped. To land a hand-made port instead, open it as a pull request of its own, as [fixes that exist only on a release line](#fixes-that-exist-only-on-a-release-line) describes, and drop the original.

The batch also leaves a pull request out of a line on its own when it cannot land there whole. A pick that would leave part of what the pull request does on `main` without effect on the line, a change to a file the line does not have, or the deletion of a file the line never had or already removed, is not made: the pull request is parked, the batch pull request lists it under "Parked" with those files, and `check` prints it as a note without failing. A file the line already has as the pull request makes it counts as there, and so does a file `main` or the line renamed, under its other name. A unit already on the open batch that does only part of its original fails `check --cut` and `backport-batch verify`, whatever its label says, until `backport-batch run --defer` takes it out. What applies of a parked pull request goes to the line as a pull request of its own, as [fixes that exist only on a release line](#fixes-that-exist-only-on-a-release-line) describes.

### What waits for the next minor

`feat`, `chore` and `docs` pull requests, and dependency bumps (scope `deps`), ship with the next minor, not in a patch release; only a maintainer's `backport/include`, below, takes one. If a class of fixes should also wait for a minor, it gets a new rule on this page; a single pull request that the rules get wrong is what the overrides below are for.

### Maintainer overrides

A maintainer overrides the rules for one pull request with a label on the merged pull request on `main`:

- `backport/include` takes it although rules 2 to 4 keep it out, a `feat` for instance, or although it merged before its line started under this policy. It still has to pick cleanly or have its conflict resolved, pass CI on the batch, and, on an older line, already be on the newer one, and a line that already carries it does not take it again. It lifts no park, as [what the batch leaves out on one line](#what-the-batch-leaves-out-on-one-line) describes.
- `backport/skip` keeps it out although the rules take it, and `backport-batch check` stops asking for it. An open batch that picked it before the label came keeps it until `backport-batch run --defer` takes it out; the batch pull request says so meanwhile.

A label decides for every line. To keep a pull request out of one line only, drop it there with `backport-batch run --drop`, as [what the batch leaves out on one line](#what-the-batch-leaves-out-on-one-line) describes; `backport/include` does not lift a drop.

`backport/skip` wins when a pull request carries both. Each run reads the labels afresh, and the batch pull request lists every override and what became of it.

### Which lines the batch runs for

The batch runs for the two newest lines that have shipped a stable release, and for every line branched after the newest of them that has not shipped one yet. A line has shipped once its first stable `vX.Y.Z` tag exists, which only the merge of its promotion creates ([stable tags come from rc promotion](./stable-tags.md)); an rc, alpha or beta tag does not count. How many released lines it keeps is `released-lines` in the block below.

With 1.6 and 1.7 released, cutting `v1.8.0-rc.1` creates `release-1.8` ([release candidates](./release-candidates.md)), and from the next run the batch runs for 1.6, 1.7 and 1.8. Once `v1.8.0` ships it runs for 1.7 and 1.8, and 1.6 takes no more batches. Rule 6 holds across all of them, newest first: while 1.8 is in its rc window, a fix reaches 1.7 only after the 1.8 batch that carries it has merged, and 1.6 only after 1.7's.

During its rc window a line is frozen: every later rc is cut from `release-X.Y` itself, and `main` is open for the next minor. The freeze closes the line to `main`, not to the batch, which is how fixes reach it: a batch pull request against a frozen line is built, checked and merged like any other, and the next rc carries what it merged once `check --cut` passes (see [Running the batch](#running-the-batch)). A line that drops out keeps its open batch pull request as it is, and `backport-batch` no longer runs or checks that line; a maintainer merges the pull request or closes it.

### Where each line starts

The policy applies in full from `release-1.7` on: a line released before it took effect keeps the patch content its users chose it with, so it takes only fixes merged after the policy started, and an older one only with `backport/include`. The batch reads the block below from this page, on `main`: a line it lists starts on that day, and any other line starts where it branched from `main`. The block also gives the `batch-only` day and `released-lines`.

From the `batch-only` day in the block, a change from `main` reaches a line only through the batch, and only the batch's picks show that a line carries it: a pick whose `(cherry picked from commit …)` line, the one git appends, names the original's merge commit or every commit of its own. A picked commit keeps its message and a batch merge carries the original's, so text in them, a `Backport of #N`, a `Backported-from:` trailer or an `-x` line the commit's author wrote, counts only on commits from before that day, where it records the hand backports and earlier batches.

```backport-policy
# release-X.Y: YYYY-MM-DD, the day the line started under this policy
release-1.6: 2026-10-26
# batch-only: YYYY-MM-DD, the day from which only the batch's picks show what a line carries
batch-only: 2026-10-26
# released-lines: N, how many of the newest lines with a stable release the batch runs for
released-lines: 2
```

### Fixes that exist only on a release line

Some fixes go to a release line as a pull request of their own. Such pull requests are occasional, and maintainers open them: for a fix with no counterpart on `main`, for one that on `main` came as part of another pull request, or for one written for `main` rather than for the line. When the counterpart on `main` is a pull request of its own that the batch would take, the batch skips it as already on the line once picking it would change nothing there; otherwise a maintainer labels it `backport/skip`. A port of only what a line has of a pull request from `main`, one that leaves out what the line lacks, is such a pull request as well: the batch carries no unit that does part of its original, and `backport-batch fold` refuses one.

A pull request opened against a `release-*` branch is closed automatically, with a comment that links to this page, when its author is a user account, not a bot, that GitHub names neither the repository's owner nor a member nor a collaborator, and whose access to the repository GitHub reports as read or none. Anyone with triage access or more keeps their pull request open. When GitHub's answer on the author's access cannot be read, the check closes nothing and fails, naming the read that failed. The check runs only when a pull request is opened, so a maintainer keeps a contributor's release-only fix by reopening it. [`hack/close-manual-backport.sh`](../../hack/close-manual-backport.sh) does this from [`pr-labeler.yaml`](../../.github/workflows/pr-labeler.yaml); run it with `--pr <number> --dry-run` to see what it would decide for a given pull request when it is opened.

### Cadence

The batch runs for each of its lines twice a week, on Monday and Thursday: often enough that a fix reaches its line within days, rarely enough that nobody spends time on it every day, and early enough in the week that each run leaves working days to cut an rc, run its e2e and promote it before the weekend. Patch releases ship weekly.

### Running the batch

[`backport-batch`](../../cmd/backport-batch/README.md) builds the batch in a clean worktree, newest line first. The scheduled run below does this; by hand it is:

```bash
go build ./cmd/backport-batch
./backport-batch run --line release-1.7 --continue --push
./backport-batch run --line release-1.6 --continue --push
```

A run by hand trusts the open batch pull request the scheduled run opened as `cozystack-ci[bot]` and continues it; `BACKPORT_BATCH_BOT` names another bot, and set empty trusts none.

`./backport-batch lines` prints the lines, newest first. `run`, `check`, `fold` and `lines` read the policy block from this page on `main`, so until the page with its block is on `main` they need `--policy <ref>` naming a ref that has it, and the scheduled run, which passes none, fails.

`run` picks what the rules take onto `backport-batch/<line>-<yyyymmdd>` and opens a draft pull request against the line. A pull request merged into `main` with a merge commit goes onto the batch as its own commits, oldest first, each picked with `git cherry-pick -x --signoff` so it keeps its author, its message and its sign-offs and gains the sign-off of whoever runs the batch; commits that merged `main` into the pull request's branch are left out. A merge commit joins them to the batch: it carries the original's merge message from `main` and ends in a `Backported-from: <merge commit>` trailer. A pull request squash-merged into `main` goes on as one pick. When any commit of an original conflicts, nothing of that original stays on the batch. The batch pull request is merged into the line with a merge commit, so all of this reaches the line as it is. Its body lists the originals with how many commits each brought and which of them differ from the commit they came from, whose release notes wait under "Check these notes" with what to compare, since a note may not hold for a pick that differs, the conflicts it deferred with the commit that failed and its files, what it parked and why, and what it skipped under which rule, and ends with the [line's record and the run's report](#the-lines-record-and-reports); `--push` prints the pull request's URL. While that pull request is open, the next run continues it, `--continue` reading its head, rather than opening another. CI on the batch pull request runs the line's build and tests, and a maintainer merges it. Since rule 6 reads the newer line itself, an older line takes a fix in the batch after the newer line's batch with that fix has merged.

A deferred conflict waits for the next batch. To land it sooner, pick the original's commits onto the batch head with `git cherry-pick -x`, resolving each conflict, and join them to the batch head with a merge commit that names the original; an original squash-merged into `main` is one pick of its commit there, with no merge. Check that merge or that pick with `./backport-batch fold --line <line> --original <number> --commit <sha>`, and push it onto the batch branch, as [resolving a conflict](./backport-tasks/resolve-conflict.md) describes. CI runs `backport-batch verify` on every push to a batch pull request, which holds every original on it to the same shape. When CI on the batch pull request fails, the original it comes from is taken out with `./backport-batch run --line <line> --defer <number> --reason <why> --push` and goes to the next batch, as [attributing a failed batch CI](./backport-tasks/failed-ci.md) describes; the rest of the batch merges without it. When it is all the batch carries, `--push` records why in the batch pull request, closes it and deletes its branch, and the next batch tries it again. `--drop` and `--undrop` change the record of the open batch pull request in the same way, and need one.

Before a patch release or an rc is cut from a line, `./backport-batch check --line <line> --cut` has to exit 0. A cut tags the line, so the check fails while a pull request the rules take is not on the line, including one that is only on the open batch pull request: merge the batch pull request first. It prints the URLs, and prints what the batch deferred as warnings, since deferring is a maintainer's call; so is a pull request labelled `backport/skip` that the open batch still carries. An original that waits on the batch for a maintainer fails the cut, also when it is all the batch has left, and so does a unit on the batch that carries only part of its original, whatever its label. A cut that passes says so. Without `--cut`, as the scheduled run uses it, a change on the open batch pull request counts as carried.

### The line's record and reports

What the batch decided about a line, the conflicts it deferred, the originals taken out with `--defer` and those waiting for a maintainer, is kept in a `backport-batch` block in the body of the line's newest batch pull request, its record. `backport-batch` reads the record whether that pull request is open, merged or closed, and writes it only while the pull request is open: a merged or closed batch pull request is never edited or commented on again. So a conflict the batch deferred is still a warning in `check` after the batch merged, until a later batch tries it again. A run that picks nothing while no batch is open writes nothing, and says so. A record `backport-batch` cannot read, or one written by someone it does not trust, stops it rather than let it fall back to an older one; how it decides is in [its README](../../cmd/backport-batch/README.md#the-lines-record-and-reports).

Each run also reports where every pull request merged into `main` since the line branched stands, and what it read to decide. The body carries the latest report under the record, and a comment the tool posts on the open batch pull request keeps each one, so an earlier classification is still there after a later run replaced the body, and after the batch merged. Each drop in a report keeps its reason and the merge commit it is bound to. `./backport-batch report --pr <batch>` prints the report the body carries, and `--id <id>` an earlier one; it writes nothing and never fetches, and reads GitHub and, for a report that keeps drops, the clone's history at the heads the report was made from. A run that picks nothing while no batch is open has nowhere to keep its report: the next batch's report has the line's state.

### Changes to workflows

The scheduled run's token may not push a commit that changes `.github/workflows/`, so the scheduled run leaves such an original out (`run --no-workflow-files`). The batch pull request lists it under "Waiting for a maintainer", with the workflow files it changes, and so does the batch's comment. It is no conflict. The policy still takes it: `check` warns about it while it waits, and `check --cut` fails on it. When nothing else is picked there is no batch pull request to list it on, and `check` reports it as missing.

A maintainer brings it over with their own token, one that may push workflow changes (the Workflows permission, or the classic `workflow` scope), by continuing the open batch without the flag:

```bash
./backport-batch run --line <line> --continue --push
```

That picks it onto the batch in the batch's shape, after what the batch carries, and the body stops listing it; later scheduled runs continue the batch on top. To cut without it instead, take it out with `./backport-batch run --line <line> --defer <number> --reason <why> --push`.

### The scheduled run

[`backport-batch.yaml`](../../.github/workflows/backport-batch.yaml) runs [`hack/backport-batch-scheduled.sh`](../../hack/backport-batch-scheduled.sh) on Monday and Thursday. For each line the batch runs for, newest first, it continues the open batch pull request or opens one, runs `check`, and then `backport-batch notify`, which reports what waits on the batch. A batch open for three days or more, so one the previous run already saw, gets a comment on its pull request saying so and a line in the run's summary. The comment is one per pull request, updated in place on every run. It also lists the deferred conflicts, which a maintainer resolves as [resolving a conflict](./backport-tasks/resolve-conflict.md) describes, and the originals that wait because they change workflows. A failed batch CI waits for a maintainer too, as [attributing a failed batch CI](./backport-tasks/failed-ci.md) describes; the comment does not list it, and it shows on the batch pull request's checks. `hack/backport-batch-scheduled.sh --dry-run` does the same in the current worktree and changes nothing on GitHub. Its `check` sees the line without the batch the dry run just built, so it can exit 1 where the real run's check passes.

The batch runs as the cozystack-ci GitHub App, which the release workflows already use, and needs no secret of its own. The job mints a token from `COZYSTACK_CI_APP_ID` and `COZYSTACK_CI_PRIVATE_KEY` for this repository alone, with Contents and Pull requests write and nothing more, and fails when either secret is missing. Its pushes start CI on the batch pull request, which a push with the job's own `GITHUB_TOKEN` would not. Its commits carry the App's bot, `cozystack-ci[bot]`, whose batch pull request and comment `backport-batch` trusts: `BACKPORT_BATCH_BOT`, which the job sets from the token, names it, and a run without the variable trusts `cozystack-ci[bot]` too. The App's installation has to grant this repository Contents and Pull requests read and write, which an organization admin sees among the organization's installed GitHub Apps. It has no Workflows permission, so a change to `.github/workflows/` waits for a maintainer, as [changes to workflows](#changes-to-workflows) describes.

It also reads one repository setting, which a repository admin sets up: `BACKPORT_ALERT_TEAM`, a variable, the team or person the comment mentions, as `org/team` or a login. Unset, the comment mentions nobody: the batch pull request is a draft the App opened, so no one is notified of what waits unless they watch the repository.

Optionally, once the `Backport batch shape` check runs from `main`, a ruleset makes it required on the release lines. It targets `release-*` and excludes `release-*.*.*`: `release-*` alone also matches the `release-X.Y.Z-rc.N` and `release-X.Y.Z` staging branches that [`tags.yaml`](../../.github/workflows/tags.yaml) and [`promote-rc.yaml`](../../.github/workflows/promote-rc.yaml) push, which have no batch on them, and a required check would refuse those pushes. "Do not require status checks on creation" stays on, since [`cut-prerelease.yaml`](../../.github/workflows/cut-prerelease.yaml) creates `release-X.Y` with a push at its first rc.

To run the scheduled script for real by hand, export your own token as `BACKPORT_BATCH_TOKEN`: without `--dry-run` it uses that variable and no other credential. With a token that may push workflow changes, leave `--no-workflow-files` out and the run brings over what waits.
