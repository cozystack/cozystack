## Backport mechanics

Under the [backport policy](./backports.md) fixes reach a release line through the batch, and nobody labels pull requests for backport. This page covers the mechanics around it: cherry-pick triage before a patch, and the trap that keeps migrations out of patch releases.

### Cherry-pick triage before a patch

A patch release includes bugfixes for code that shipped in the corresponding minor `vX.Y.0`. What goes in is decided by the rules in [Backports](./backports.md), not per pull request, and each batch pull request says what it picked, what it deferred and what it skipped under which rule. What is left to check before a cut:

```bash
# Exits 1 and prints the URLs while a pull request the rules take is neither
# on the line nor on the open batch PR; see cmd/backport-batch/README.md.
go build ./cmd/backport-batch
./backport-batch check --line release-X.Y
```

[`backport-batch check`](../../cmd/backport-batch/README.md) applies the rules to every pull request merged into `main` since the line branched, and looks for each one they take on the line and then on the open batch pull request: by picks whose `-x` lines name its merge commit or every commit of its own, by a pick that would change nothing, and, on commits from before the policy's batch-only day, by what their text says, such as `Backport of #N`. A conflict the batch deferred prints as a warning without failing the check. A change that reached the line by hand counts like any other, by what reached the line.

### Backporting code that touches monotonic counters

The batch never takes a change like this: rule 4 in [Backports](./backports.md) holds migrations, `targetVersion`, sizeable API changes and CRDs for the next minor. What follows is why, and what a maintainer has to do when a migration must reach a release line anyway.

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
