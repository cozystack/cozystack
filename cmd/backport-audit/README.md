# backport-audit

Answers one question, before a release or an rc is cut: did everything labelled for backport actually land on the release branch? It prints the URLs of the ones that did not, and exits non-zero, so it can gate the cut.

The manual alternative is `gh pr list --search "is:merged label:kind/backport"`, which lists every PR ever labelled and says nothing about whether the change reached the branch. Answering that by hand means reading the branch history per PR, which is where backports quietly go missing.

```console
$ go run ./cmd/backport-audit release-1.5

=== release-1.5 === 43 candidate PRs: MISSING=2 pending=3 dropped=5 backported=33

  MISSING -- labelled for this line, no trace of it here (2):
    https://github.com/cozystack/cozystack/pull/3449
      #3449 ci(release): move release validation to rc time — e2e lane, changelog button, promote gate, docs at promote
      label=kind/backport-previous author=myasnikovdaniil merged=2026-07-29 -- no backport PR, nothing on branch
```

The URL is always the **original PR on main** — the thing you decide about. The evidence line names the backport PR when one exists. Anything already on the branch is counted in the header and never listed, so silence means done.

## Usage

```bash
go run ./cmd/backport-audit                      # the two newest release lines
go run ./cmd/backport-audit release-1.5          # one line
go run ./cmd/backport-audit release-1.6 release-1.5 --json
```

Requires `git` and an authenticated `gh` on PATH. The audit is read-only: it fetches, queries the GitHub API, and writes nothing.

| Flag | Meaning |
|------|---------|
| `--remote NAME` | Git remote holding the release branches (default `origin`) |
| `--limit N` | Max merged PRs scanned per label spelling (default 1000) |
| `--no-fetch` | Trust the local refs as-is, skipping `git fetch` |
| `--json` | Machine-readable output |
| `--no-color` | Disable color (auto-disabled on a non-TTY and under `--json`) |

Arguments and flags may be given in any order.

## Exit code

`0` when nothing is outstanding, `1` when something is, `2` when the audit could not be completed. That is the point of the tool, so it holds in `--json` mode too:

```bash
go run ./cmd/backport-audit release-1.5 || echo "do not cut yet"
```

`go run` forwards the exit code but also prints its own `exit status 1` line to stderr. Build the binary first (`go build ./cmd/backport-audit`) where that noise is unwelcome.

Exit `2` covers the cases where an answer cannot be trusted rather than merely being bad news, and a saturated listing is one of them: `gh` truncates at `--limit` in silence, and a truncated list does not make the audit partial, it makes it wrong — the PRs past the cut are reported nowhere and the exit code says clean. Each of the three listings is checked against its cap and fails instead, naming the cap to raise.

## Statuses

| Status | Meaning | Action |
|--------|---------|--------|
| `in-branch` | The PR merged before the branch was cut, so its change is already there | none, no backport was ever needed |
| `backported` | A merged backport PR, or the change is present in the branch's history | none |
| `pending` | A backport PR exists and is still open, including the drafts the bot opens with the conflict committed | merge it, or finish the draft |
| `dropped` | A backport PR was closed unmerged, reported with whatever reason someone left on it | none if the reason still holds |
| `MISSING` | No backport PR ever existed and nothing on the branch matches | cherry-pick it by hand, or drop it deliberately |

`dropped` is usually healthy — a maintainer deciding a fix does not apply to that line, e.g. because the feature it repairs never shipped there. The recorded reason is printed so the next release does not re-open the same investigation. A `dropped` entry reading `no reason recorded` is the one that needs a human.

## How a PR is matched to a release line

A `kind/backport` label does not name a branch. [`backport.yaml`](../../.github/workflows/backport.yaml) resolves the target **at merge time**, so the same label means `release-1.5` on a PR merged in June and `release-1.6` on one merged in August. The audit reproduces that rule rather than trusting the label text: a PR is a candidate for `release-X.Y` when it merged carrying `kind/backport` while `X.Y` was the current line, or `kind/backport-previous` while `X.Y` was one minor behind. Without this, auditing an older line reports every newer-era PR as missing.

What "the current line" means changed once, so the audit carries both rules and picks by the PR's merge date:

| PR merged | rule `backport.yaml` was running | a line exists from |
|-----------|----------------------------------|--------------------|
| before 2026-08-03 | `getLatestRelease` | its first non-prerelease release publishing |
| on or after 2026-08-03 | the two newest existing `release-X.Y` branches | its `release-X.Y` branch being pushed |

The cutover is a single moment because both halves landed in one push: 53a7fc4dc made cutting an rc freeze the line into `release-X.Y`, and 6ff5b98d5 pointed the bot at the branch list. Before that a release branch was created at promote time, so the newest branch and the newest published stable named the same line and the two rules could not disagree. After it they disagree for the length of every freeze window — `release-X.Y` is created when the first `vX.Y.0-rc.N` is cut, potentially weeks before `vX.Y.0` publishes, and everything labelled in between belongs to the line being stabilised. That window is exactly when a backport has to reach the branch, because the frozen branch is the only way into the release.

The branch-push moment is read exactly rather than approximated. [`cut-prerelease.yaml`](../../.github/workflows/cut-prerelease.yaml) creates the branch **at the tagged commit**, in the same job and immediately after the tag push, and the stale-tip guard just above that push refuses to proceed unless the dispatched commit is still `main`'s tip. So nothing merges to `main` between the tagged commit and the branch appearing, and that commit's own date splits every PR correctly: merged before it, the branch did not exist; merged after it, it did. The rc release's `publishedAt` would not do, since it lands hours later once `tags.yaml` has finished building.

Lines are then ranked by version and never by when they opened, matching the numeric descending sort the workflow applies to its branch list. The two orders come apart as soon as a freeze overlaps the previous line's stabilisation, and version order is also what keeps `release-1.10` above `release-1.9`.

The labels themselves were namespaced under `kind/` in 7b71053a0, which renamed them in place via label-sync aliases, so every historical PR reads back under the new name. The bare `backport` and `backport-previous` are still queried alongside them, because the org-level dosubot goes on applying the old spelling to new PRs — the same transitional allowance [`backport.yaml`](../../.github/workflows/backport.yaml) makes, and it retires at the same time. A PR found under either spelling is reported under the `kind/` one.

## How landing is established

Three independent kinds of evidence, strongest first:

1. **Reachability.** The PR's merge commit is reachable from the release branch, i.e. it merged before the branch was cut (or `main` was later merged in). Nothing was ever needed.
2. **A linked backport PR.** Found by the bot's head branch `backport-<N>-to-release-X.Y`, or by a `Backport of #N` reference in the body, which is what a hand-written backport carries. `MERGED` settles it; `OPEN` is `pending`; `CLOSED` is `dropped`.
3. **The branch's own history.** The bot's `[Backport release-X.Y] <title>` merge subject, a commit subject identical to one of the PR's, or an `-x` cherry-pick reference to one of its commits. This is what catches a hand-backport nobody linked.

## Machine-readable output

```bash
# URLs of what never landed, ready to paste into a tracking issue
go run ./cmd/backport-audit --json release-1.5 | jq -r '.[][] | select(.status=="MISSING") | .url'

# every open backport PR to go merge, across lines
go run ./cmd/backport-audit --json release-1.6 release-1.5 \
  | jq -r '.[][] | select(.status=="pending") | .backport_prs[].url'

# dropped items with the reason someone recorded
go run ./cmd/backport-audit --json release-1.4 \
  | jq -r '.[][] | select(.status=="dropped") | "#\(.number)\t\(.reason)"'
```

Output is an object keyed by branch, each holding one record per candidate:

```json
{
  "number": 3963,
  "title": "fix(clickhouse): scheme the backup S3_ENDPOINT on the system-bucket flow",
  "url": "https://github.com/cozystack/cozystack/pull/3963",
  "author": "androndo",
  "mergedAt": "2026-08-28T11:05:15Z",
  "label": "kind/backport",
  "backport_prs": [{"number": 3987, "state": "OPEN", "url": "..."}],
  "status": "pending",
  "evidence": "backport PR #3987 open"
}
```

## Limits

A `MISSING` verdict is a prompt to check, not proof of absence. A hand-backport that was squash-merged, reworded, and referenced no original PR is invisible to every evidence layer and reads as `MISSING`; confirm at diff level before redoing the work. A `kind/backport` label added long after merge, once the release line has moved on, resolves to the newer line. And the audit reports *that* something is missing, never *why* — a run of `MISSING` entries clustered in time usually means the bot itself was failing during that window, which is worth checking before cherry-picking them one by one.
