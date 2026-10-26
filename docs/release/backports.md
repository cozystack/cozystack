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
5. It picks cleanly with `git cherry-pick -x`, or its conflict is resolved during the batch and the batch's build and tests pass. A pick that fails moves to the next batch, and the batch pull request lists it with the files that conflicted.
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

```backport-policy
# release-X.Y: YYYY-MM-DD, the day the line started under this policy
release-1.6: 2026-10-26
```

### Fixes that exist only on a release line

A fix that only makes sense on a release line, with no counterpart on `main`, goes to that line as a pull request of its own, which a maintainer keeps open.

A pull request opened against a `release-*` branch is closed automatically, with a comment that links to this page, when its author is not a bot and GitHub reports the author's access to the repository as read or none. Anyone with triage access or more keeps their pull request open, and so does everyone when GitHub's answer cannot be read. The check runs only when a pull request is opened, so a maintainer keeps a contributor's release-only fix by reopening it. [`hack/close-manual-backport.sh`](../../hack/close-manual-backport.sh) does this from [`pr-labeler.yaml`](../../.github/workflows/pr-labeler.yaml); run it with `--pr <number> --dry-run` to see what it would decide for a given pull request when it is opened.

### Cadence

The batch runs for each supported release line twice a week, on Monday and Thursday: often enough that a fix reaches its line within days, rarely enough that nobody spends time on it every day, and early enough in the week that each run leaves working days to cut an rc, run its e2e and promote it before the weekend. Patch releases ship weekly.

### Running the batch

[`backport-batch`](../../cmd/backport-batch/README.md) builds the batch in a clean worktree, newest line first:

```bash
go build ./cmd/backport-batch
./backport-batch run --line release-1.7 --push
./backport-batch run --line release-1.6 --push
```

`run` picks what the rules take onto `backport-batch/<line>-<yyyymmdd>` and opens a draft pull request against the line. Its body lists the originals, the conflicts it deferred with their files, and what it skipped under which rule. While that pull request is open, the next run continues it with `--onto <its head>` rather than opening another. CI on the batch pull request runs the line's build and tests, and a maintainer merges it. Since rule 6 reads the newer line itself, an older line takes a fix in the batch after the newer line's batch with that fix has merged.

A deferred conflict waits for the next batch. To land it sooner, resolve it with `git cherry-pick -x -m 1 <merge commit>` on top of the batch head, check the commit with `./backport-batch fold --line <line> --original <number> --commit <sha>`, and push it onto the batch branch.

Before a patch release or an rc is cut from a line, `./backport-batch check --line <line>` has to exit 0. It exits 1 and prints the URLs while a pull request the rules take is neither on the line nor on the open batch pull request, and prints the batch's deferred conflicts as warnings.
