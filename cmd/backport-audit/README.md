# backport-audit

Answers one question, before a release or an rc is cut: did everything labelled for backport actually land on the release branch? It prints the URLs of the ones that did not, and exits non-zero, so it can gate the cut.

The manual alternative is `gh pr list --search "is:merged label:backport"`, which lists every PR ever labelled and says nothing about whether the change reached the branch. Answering that by hand means reading the branch history per PR, which is where backports quietly go missing.

```console
$ go run ./cmd/backport-audit release-1.5

=== release-1.5 === 33 candidate PRs: MISSING=4 pending=9 dropped=3 backported=17

  MISSING -- labelled for this line, no trace of it here (4):
    https://github.com/cozystack/cozystack/pull/2936
      #2936 fix(kubernetes): make the default md0 node group removable
      label=backport author=myasnikovdaniil merged=2026-06-30 -- no backport PR, nothing on branch
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
| `--limit N` | Max merged PRs scanned per label (default 400) |
| `--no-fetch` | Trust the local refs as-is, skipping `git fetch` |
| `--json` | Machine-readable output |
| `--no-color` | Disable color (auto-disabled on a non-TTY and under `--json`) |

Arguments and flags may be given in any order.

## Exit code

`0` when nothing is outstanding, `1` when something is. That is the point of the tool, so it holds in `--json` mode too:

```bash
go run ./cmd/backport-audit release-1.5 || echo "do not cut yet"
```

`go run` forwards the exit code but also prints its own `exit status 1` line to stderr. Build the binary first (`go build ./cmd/backport-audit`) where that noise is unwelcome.

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

A `backport` label does not name a branch. [`backport.yaml`](../../.github/workflows/backport.yaml) resolves the target from `getLatestRelease` **at merge time**, so the same label means `release-1.5` on a PR merged in June and `release-1.6` on one merged in August.

The audit reproduces that rule rather than trusting the label text: a PR is a candidate for `release-X.Y` when it merged carrying `backport` while `X.Y` was the current line, or `backport-previous` while `X.Y` was one minor behind. A line is treated as current from the publication of its first non-prerelease release, which is what `getLatestRelease` returns. Without this, auditing an older line reports every newer-era `backport` PR as missing.

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
  "number": 3372,
  "title": "fix(keycloak-configure): patch HelmRelease in release namespace on teardown",
  "url": "https://github.com/cozystack/cozystack/pull/3372",
  "author": "lexfrei",
  "mergedAt": "2026-07-28T15:45:47Z",
  "label": "backport",
  "backport_prs": [{"number": 3478, "state": "OPEN", "url": "..."}],
  "status": "pending",
  "evidence": "backport PR #3478 open"
}
```

## Limits

A `MISSING` verdict is a prompt to check, not proof of absence. A hand-backport that was squash-merged, reworded, and referenced no original PR is invisible to every evidence layer and reads as `MISSING`; confirm at diff level before redoing the work. A `backport` label added long after merge, once the release line has moved on, resolves to the newer line. And the audit reports *that* something is missing, never *why* — a run of `MISSING` entries clustered in time usually means the bot itself was failing during that window, which is worth checking before cherry-picking them one by one.
