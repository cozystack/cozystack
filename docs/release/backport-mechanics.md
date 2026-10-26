## Backport mechanics

Under the [backport policy](./backports.md) fixes reach a release line through the batch, and nobody labels pull requests for backport. This page covers the mechanics around it: the label-driven bot that maintainers may still use as a manual fallback, cherry-pick triage before a patch, and the trap that keeps migrations out of patch releases.

### The backport bot

[`backport.yaml`](../../.github/workflows/backport.yaml) wraps [`korthout/backport-action`](https://github.com/korthout/backport-action) and fires on a merged `main`-targeted PR carrying one of:

| Label | Target branch |
|-------|---------------|
| `kind/backport` | the newest existing `release-X.Y` branch |
| `kind/backport-previous` | the second-newest existing `release-X.Y` branch |

Resolution is dynamic at run time, and it reads the branches themselves: the job lists the repository's branches, keeps the ones matching `release-<major>.<minor>`, and sorts them numerically descending — so `release-1.10` ranks above `release-1.9`, which a lexicographic sort gets backwards. `backport` takes the first, `backport-previous` the second, so both name a branch that exists by construction; asking for `backport-previous` when only one line exists fails the job rather than inventing a target. Nothing is derived arithmetically — an earlier version computed the previous line as `Y-1`, which named a non-existent branch whenever a minor was skipped.

**During a freeze window the newest line is the one being stabilised, not the last published one.** `release-X.Y` is created when the first `vX.Y.0-rc.1` is cut, before `vX.Y.0` is published, so from the freeze until the release `backport` targets the release being stabilised and `backport-previous` targets the last published stable. That is the intent: the frozen branch is the only way into the upcoming release, which is when backports matter most. The trade-off is that for the length of the window the line one step further back has no label pointing at it, so a fix that has to reach it before the new release publishes must be cherry-picked by hand.

The bot creates a backport PR with title `[Backport release-X.Y] <original title>`. When this PR merges, the title prefix used to re-trigger the bot through `pr-labeler.yaml`, which auto-applied `backport` to any `[Backport release-X.Y]`-titled PR. Combined with the org-level `dosubot` re-applying the label, this caused recursive backports.

The fix (PR #2584): both job `if:` blocks gate on `github.event.pull_request.base.ref == 'main'`. Backport PRs target `release-X.Y`, so they cannot satisfy this — architectural protection, regardless of which bot relabels them.

### When the bot fails

If the run triggered by the merge dies without opening a backport PR, look for a run of this workflow that has not finished before retrying — `gh run list --workflow backport.yaml --branch <the PR's head branch>` — and clear it, because the retry queues behind it rather than replacing it. Do not filter by status: a run whose jobs are waiting for a runner, or which is held behind the concurrency group, is not `in_progress`, and filtering it out is how you conclude nothing is running. `--branch` narrows the list but does not identify the PR, since head branch names are not unique across forks and `gh run list` has no PR filter, so confirm which PR a run belongs to before cancelling it or you cancel someone else's backport. If cancelling does not clear the run it holds the group until GitHub retires it and the retry keeps queueing; that case has not been exercised here. A run whose jobs have all finished can still sit `in_progress`, which looks the same from the PR as a run that died. Then remove whichever backport label the PR already carries and add it back: a label event is the only other trigger, and it qualifies on the label it carries, so re-applying it is what starts a fresh attempt.

Conflicting cherry-picks produce a draft PR via `conflict_resolution: draft_commit_conflicts`. Look for the bot's comment with the merge-conflict diff. You either:

- Resolve in the draft branch and undraft, or
- Drop the bot's branch and cherry-pick manually:

```bash
git checkout release-X.Y
git cherry-pick -x -s <commit-sha>
# resolve conflicts
git commit -s
git push origin release-X.Y  # or push to a new branch and open a PR
```

To find the bot's failed comments across a batch of PRs:

```bash
for n in $(gh pr list --search "label:kind/backport label:kind/backport-previous merged:>=2026-01-01" --json number --jq '.[].number'); do
  echo "=== #$n ==="
  gh pr view $n --json comments --jq '.comments[] | select(.author.login == "github-actions" or (.author.login | contains("backport"))) | .body' | head -20
done
```

Before [PR #3155](https://github.com/cozystack/cozystack/pull/3155), `conflict_resolution` was passed outside `experimental`, so a conflicting cherry-pick used the `fail` fallback and left only a "Backport failed" comment on the source PR. No draft backport PR was opened. When a labelled pull request never reached its line, check those comments and the workflow logs to establish why.

### Cherry-pick triage before a patch

A patch release includes bugfixes for code that shipped in the corresponding minor `vX.Y.0`. What goes in is decided by the rules in [Backports](./backports.md), not per pull request, and each batch pull request says what it picked, what it deferred and what it skipped under which rule. What is left to check before a cut:

```bash
# Exits 1 and prints the URLs while a pull request the rules take is neither
# on the line nor on the open batch PR; see cmd/backport-batch/README.md.
go build ./cmd/backport-batch
./backport-batch check --line release-X.Y
```

[`backport-batch check`](../../cmd/backport-batch/README.md) applies the rules to every pull request merged into `main` since the line branched, and looks for each one they take on the line, by an `-x` line naming it, a `Backport of #N` line, or a pick that would change nothing, and then on the open batch pull request. A conflict the batch deferred prints as a warning without failing the check. A change sent through the manual fallback labels counts like any other, by what reached the line rather than by its label.

### Backporting code that touches monotonic counters

The batch never takes a change like this: rule 4 in [Backports](./backports.md) holds migrations, `targetVersion`, `api/` and CRDs for the next minor. What follows is why, and what a maintainer has to do when a migration must reach a release line anyway.

Migrations are the canonical case. `packages/core/platform/images/migrations/run-migrations.sh` is a linear counter — it runs `seq $CURRENT_VERSION $((TARGET_VERSION - 1))` and stops if `CURRENT >= TARGET`. This creates a forward-upgrade trap. Walking through it with `release-1.2` as the maintenance branch:

- `main` has `targetVersion=10` and migrations 1–9 on disk.
- The `release-1.2` branch was cut when `targetVersion=5` (migrations 1–4 on disk).
- You backport a new migration to `release-1.2` at slot 5, and naively bump `targetVersion` 5 → 6 to enable it.
- Customer running 1.2.x runs the new migration, stamps `cozystack-version=6` in-cluster.
- On upgrade to 1.3 (main's `targetVersion=10`), the runner walks `seq 6 9` and runs main's migrations 6–9 — **main's own migration at slot 5 never runs.** The backport claimed that slot on `release-1.2`; main's slot 5 holds an unrelated migration that gets silently skipped.

Mitigations, in order of preference:

1. **Don't bump `targetVersion` in the backport.** Just ship the migration file. Prior precedent in the repo: an earlier maintenance branch's ACME backport did exactly this — the migration shipped but ran on no clusters until a later commit raised the counter. Pair this with relaxing the chart-side `{{ fail }}` guard on the maintenance branch (chart-side coalesce) so the new value works without the migration having executed.
2. **If you must bump,** add an idempotent duplicate of the now-skipped main migration at a higher slot. Bounded cost (one extra file per affected migration) and survives the skip window.
3. **Audit cross-branch counter state before deciding.** Compare migration numbering across `main`, `release-X.Y`, and `release-X.(Y-1)`. If a backport would create a skip window on any forward-upgrade path, choose option 1.

This rule generalizes to any monotonic-counter state: schema versions, feature-flag generations, anything that gates "have I run X yet?" with a single integer.
