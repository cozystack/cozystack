## Backports

Backports are automatic. Everything that can go into a patch release goes in, and nobody decides it pull request by pull request: nobody requests a backport, and contributors never open backport pull requests. The one exception is a maintainer override, below. A batch job takes the fixes merged into `main` and cherry-picks them onto each supported `release-X.Y` line, following the rules on this page.

### Getting a fix into a patch release

Merge it into `main` with a `fix` title, for example `fix(postgres): keep the replica count on resize`. Nothing else is needed: if the change meets the rules below, the next batch for each supported line picks it up.

The batch takes a pull request's type from its title alone, so a fix that ships inside a `feat` or `refactor` pull request does not reach a patch release. Put the fix in a pull request of its own.

### What the batch takes

The batch takes a pull request from `main` into release line X.Y when all of these hold:

1. It merged into `main` after `release-X.Y` branched, or after the line started under this policy if that is later (see [Where each line starts](#where-each-line-starts)), and the line does not carry it yet.
2. Its title type is `fix` or `security`. CI and e2e fixes count, because they keep the line's own CI working.
3. It is not breaking: no `!` after the type or scope, and no `BREAKING CHANGE` footer.
4. It touches no platform migrations and no `targetVersion`, adds, removes or edits no CustomResourceDefinition document (a CRD in a test fixture under `tests/` or `testdata/` does not count, and neither does an edit to another document in the same file), and makes no change to the API that the [API review gate](../../.github/workflows/api-review-gate.yaml) calls sizeable: a new or removed API group or resource, or a schema change that can reject what the old schema accepted. Those wait for the next minor; a reworded description or a new optional field under `api/` does not. [Backporting code that touches monotonic counters](./backport-mechanics.md#backporting-code-that-touches-monotonic-counters) shows what a migration on a release line breaks.
5. Each of its commits picks cleanly with `git cherry-pick -x`, or its conflict is resolved during the batch, and the batch's build and tests pass. A pick that fails moves to the next batch, and the batch pull request lists it with the files that conflicted.
6. On an older line, it is already on the next newer line. Lines are processed newest first, so an older line takes only what the newer one already carries.

### What never goes into a patch release

`feat`, `chore` and `docs` pull requests, and dependency bumps (scope `deps`), never go into a patch release. They ship with the next minor. If a class of fixes should also wait for a minor, it gets a new rule on this page; a single pull request that the rules get wrong is what the overrides below are for.

### Maintainer overrides

A maintainer overrides the rules for one pull request with a label on the merged pull request on `main`:

- `backport/include` takes it although rules 2 to 4 keep it out, a `feat` for instance, or although it merged before its line started under this policy. It still has to pick cleanly or have its conflict resolved, pass CI on the batch, and, on an older line, already be on the newer one, and a line that already carries it does not take it again.
- `backport/skip` keeps it out although the rules take it, and `backport-batch check` stops asking for it. An open batch that picked it before the label came keeps it until `backport-batch run --defer` takes it out; the batch pull request says so meanwhile.

`backport/skip` wins when a pull request carries both. Each run reads the labels afresh, and the batch pull request lists every override and what became of it.

### Where each line starts

The policy applies in full from `release-1.7` on: a line released before it took effect keeps the patch content its users chose it with, so it takes only fixes merged after the policy started, and an older one only with `backport/include`. The batch reads the block below from this page, on `main`: a line it lists starts on that day, and any other line starts where it branched from `main`.

From the `batch-only` day in the block, the batch is the only way a change reaches a line, and only what it does shows that a line carries a change: a pick whose `(cherry picked from commit …)` line, the one git appends, names the original's merge commit or every commit of its own. A picked commit keeps its message and a batch merge carries the original's, so text in them, a `Backport of #N`, a `Backported-from:` trailer or an `-x` line the commit's author wrote, counts only on commits from before that day, where it records the hand backports and earlier batches.

```backport-policy
# release-X.Y: YYYY-MM-DD, the day the line started under this policy
release-1.6: 2026-10-26
# batch-only: YYYY-MM-DD, the day from which only the batch's picks show what a line carries
batch-only: 2026-10-26
```

### Fixes that exist only on a release line

A fix that only makes sense on a release line, with no counterpart on `main`, goes to that line as a pull request of its own, which a maintainer keeps open.

A pull request opened against a `release-*` branch is closed automatically, with a comment that links to this page, when its author is not a bot and GitHub reports the author's access to the repository as read or none. Anyone with triage access or more keeps their pull request open, and so does everyone when GitHub's answer cannot be read. The check runs only when a pull request is opened, so a maintainer keeps a contributor's release-only fix by reopening it. [`hack/close-manual-backport.sh`](../../hack/close-manual-backport.sh) does this from [`pr-labeler.yaml`](../../.github/workflows/pr-labeler.yaml); run it with `--pr <number> --dry-run` to see what it would decide for a given pull request when it is opened.

### Cadence

The batch runs for each supported release line twice a week, on Monday and Thursday: often enough that a fix reaches its line within days, rarely enough that nobody spends time on it every day, and early enough in the week that each run leaves working days to cut an rc, run its e2e and promote it before the weekend. Patch releases ship weekly.

### Running the batch

[`backport-batch`](../../cmd/backport-batch/README.md) builds the batch in a clean worktree, newest line first. The scheduled run below does this; by hand it is:

```bash
go build ./cmd/backport-batch
./backport-batch run --line release-1.7 --continue --push
./backport-batch run --line release-1.6 --continue --push
```

`run` picks what the rules take onto `backport-batch/<line>-<yyyymmdd>` and opens a draft pull request against the line. A pull request merged into `main` with a merge commit goes onto the batch as its own commits, oldest first, each picked with `git cherry-pick -x` so it keeps its author, its message and its sign-offs; commits that merged `main` into the pull request's branch are left out. A merge commit joins them to the batch: it carries the original's merge message from `main` and ends in a `Backported-from: <merge commit>` trailer. A pull request squash-merged into `main` goes on as one pick. When any commit of an original conflicts, nothing of that original stays on the batch. The batch pull request is merged into the line with a merge commit, so all of this reaches the line as it is. Its body lists the originals with how many commits each brought and which of them differ from the commit they came from, the conflicts it deferred with the commit that failed and its files, and what it skipped under which rule. While that pull request is open, the next run continues it, `--continue` reading its head, rather than opening another. CI on the batch pull request runs the line's build and tests, and a maintainer merges it. Since rule 6 reads the newer line itself, an older line takes a fix in the batch after the newer line's batch with that fix has merged.

A deferred conflict waits for the next batch. To land it sooner, pick the original's commits onto the batch head with `git cherry-pick -x`, resolving each conflict, join them to the batch head with a merge commit that names the original, check that merge with `./backport-batch fold --line <line> --original <number> --commit <sha>`, and push it onto the batch branch, as [resolving a conflict](./backport-tasks/resolve-conflict.md) describes. CI runs `backport-batch verify` on every push to a batch pull request, which holds every original on it to the same shape. When CI on the batch pull request fails, the original it comes from is taken out with `./backport-batch run --line <line> --defer <number> --reason <why> --push` and goes to the next batch, as [attributing a failed batch CI](./backport-tasks/failed-ci.md) describes; the rest of the batch merges without it.

Before a patch release or an rc is cut from a line, `./backport-batch check --line <line>` has to exit 0. It exits 1 and prints the URLs while a pull request the rules take is neither on the line nor on the open batch pull request, and prints what the batch deferred as warnings.

### The scheduled run

[`backport-batch.yaml`](../../.github/workflows/backport-batch.yaml) runs [`hack/backport-batch-scheduled.sh`](../../hack/backport-batch-scheduled.sh) on Monday and Thursday. For each line the batch runs for, newest first, it continues the open batch pull request or opens one, runs `check`, and then `backport-batch notify`, which reports what waits on the batch. A batch open for three days or more, so one the previous run already saw, gets a comment on its pull request saying so and a line in the run's summary. The comment is one per pull request, updated in place on every run. Deferred conflicts go to whoever `BACKPORT_AGENT` names. `hack/backport-batch-scheduled.sh --dry-run` does the same in the current worktree and changes nothing on GitHub.

It reads these repository settings, which a repository admin sets up:

- `BACKPORT_BATCH_TOKEN`, a secret, required: the token the batch pushes `backport-batch/*` branches, opens and edits its pull requests and comments with, from an account with write access. A fine-grained token on this repository needs contents, pull requests and workflows read and write; workflows because a fix to CI changes `.github/workflows/`. The job fails without it rather than use its own `GITHUB_TOKEN`, since a push made with that token starts no CI on the batch pull request.
- `BACKPORT_AGENT`, a variable: `ci` hands each deferred conflict to the Copilot cloud agent as a task on the batch branch, through the agent tasks API, with the [resolving a conflict](./backport-tasks/resolve-conflict.md) instructions; `manual`, the default, lists the conflicts in the batch's comment for a maintainer. A failed batch CI waits for a maintainer in both modes, as [attributing a failed batch CI](./backport-tasks/failed-ci.md) describes.
- `BACKPORT_AGENT_TOKEN`, a secret, needed with `ci`: the token the agent tasks start with. The agent tasks API takes a person's token only, never an installation token such as `GITHUB_TOKEN`: a fine-grained token on this repository with agent tasks read and write and nothing else, from an account with a Copilot seat and write access.
- `BACKPORT_ALERT_TEAM`, a variable, optional: the team the comment mentions, as `org/team`. Unset, the comment mentions nobody.
