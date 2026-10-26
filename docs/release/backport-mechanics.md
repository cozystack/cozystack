## Backports

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

Before [PR #3155](https://github.com/cozystack/cozystack/pull/3155), `conflict_resolution` was passed outside `experimental`, so a conflicting cherry-pick used the `fail` fallback and left only a "Backport failed" comment on the source PR. No draft backport PR was opened. When [`backport-audit`](../../cmd/backport-audit/README.md) reports `MISSING`, check those comments and the workflow logs to establish why the change did not arrive.

### Cherry-pick triage before a patch

A patch release includes bugfixes for code that shipped in the corresponding minor `vX.Y.0`. Use:

```bash
# 1. Audit what was labeled for this line and has NOT landed on it.
#    Exits non-zero when anything is outstanding; see cmd/backport-audit/README.md.
go build ./cmd/backport-audit
./backport-audit release-X.Y

# 2. List commits on main since the release branch diverged that are NOT yet on release-X.Y
git merge-base origin/main origin/release-X.Y
git log <base>..origin/main --grep="(#" --oneline

# 3. Open PRs that may need labeling before the cut
gh pr list --state open --base main --label kind/bug
```

Step 1 covers the currently labeled work: [`backport-audit`](../../cmd/backport-audit/README.md) assigns each request to the release line selected at merge time, then checks whether its commits reached the branch. Incomplete delivery, open or dropped backports, missing changes and duplicate open claims fail the gate. Late-added labels can disagree with the bot's target, and branch-opening dates are estimated from rc tags; see the audit's documented limits before deciding what to backport. Steps 2 and 3 cover changes that were never labeled.

**Include rule:**

- `kind/bug` — especially destructive (data loss, crash-loop, OOM, eviction).
- `area/security` / CVE.
- Narrow chart fixes whose blast radius is contained (e.g. one operator's resources).
- Patch-line-policy-matching dependency bumps (e.g. cilium 1.X.Y → 1.X.(Y+1) on a `release-A.B` shipping cilium 1.X — never a minor bump, only a patch within the line the branch already ships).

**Skip rule:**

- `kind/feature` — even with `lgtm`. A patch release is not a delivery vehicle for features.
- CI-only changes (`area/ci`, test infrastructure).
- Docs-only changes (those land via `update-website-docs` automatically).
- Large multi-package dependency churn (Dependabot Go-deps bumps).
- PRs whose conventional-commit type is `!` (breaking) or that mix `feat` with `fix`.

**Borderline cases:**

- Large refactors with `lgtm` AND an obvious bug payload — prefer extracting the bug fix into a narrower commit and backporting only that.
- PRs labeled both `kind/bug` and `kind/feature` — split them.

### Backporting code that touches monotonic counters

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
