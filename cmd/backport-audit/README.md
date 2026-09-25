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

The URL is always the **original PR on main** — the thing you decide about. The evidence line names the backport PR when one exists. A labelled PR already on the branch is counted in the header and never listed, so silence under the outstanding headings means done. Two more sections, described in [Backports the labels do not account for](#backports-the-labels-do-not-account-for), start from the backport PRs on the branch instead of from labels: `DUPLICATE`, which is outstanding, and `UNLABELLED`, which is informational.

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
| `--limit N` | Max merged PRs scanned per label spelling (default 1000, which is also the ceiling — see [Exit code](#exit-code)) |
| `--no-fetch` | Trust the local refs as-is, skipping `git fetch` |
| `--json` | Machine-readable output |
| `--no-color` | Disable color (auto-disabled on a non-TTY and under `--json`) |

Arguments and flags may be given in any order.

## Exit code

`0` when nothing is outstanding, `1` when something is, `2` when the audit could not be completed. Outstanding means a `MISSING`, `partial`, `unverified`, `pending` or `dropped` verdict, or any `DUPLICATE` entry; `UNLABELLED` entries never move the exit code. That is the point of the tool, so it holds in `--json` mode too:

```bash
go build ./cmd/backport-audit
./backport-audit release-1.5
case $? in
  0) echo "clean" ;;
  1) echo "do not cut yet" ;;
  *) echo "the audit itself failed; no answer yet" ;;
esac
```

Use the built binary (`go build` as above, or `go install ./cmd/backport-audit`) wherever the exit code matters. `go run` does not preserve it: any non-zero exit of the program comes back as `go run`'s own `1`, after an `exit status N` line on stderr, so under `go run` an audit that could not be completed looks exactly like one that found outstanding work. `go run` is fine for reading the report.

Exit `2` covers the cases where an answer cannot be trusted rather than merely being bad news, and a saturated listing is one of them: `gh` truncates at `--limit` in silence, and a truncated list does not make the audit partial, it makes it wrong — the PRs past the cut are reported nowhere and the exit code says clean. Each of the three listings is checked against its cap and fails instead, naming the cap to raise. The label listings go through GitHub search, which never returns more than 1000 results however far it is paginated, so they are checked against the lower of `--limit` and 1000: a `--limit` above 1000 cannot make a listing cut at 1000 pass for complete. A git read that a verdict depends on is the other case: when one fails, a merge commit missing from the local repository for instance, the audit stops with `2` instead of judging the PR without it.

## Statuses

| Status | Meaning | Action |
|--------|---------|--------|
| `in-branch` | The PR merged before the branch was cut, so its change is already there | none, no backport was ever needed |
| `backported` | Every commit of the PR is on the branch, or, for a PR of a single commit, a backport PR merged | none |
| `confirmed` | Would be `partial` or `unverified`, but a maintainer attested the backport complete on the merged backport PR; the report names who and links the comment | none |
| `partial` | Some of the PR's commits are on the branch and some are not, listed with SHA and subject — a merged bot conflict draft, which stops at the first commit that did not apply, or a backport squashed by hand | backport the missing commits, or [attest the backport complete](#attesting-a-backport-complete) |
| `unverified` | A backport PR merged, but none of the PR's several commits is on the branch under its own SHA, `-x` reference or subject — typically a backport squashed or reworded by hand | check it, then attest it complete or backport what is missing |
| `pending` | A backport PR exists and is still open, including the drafts the bot opens with the conflict committed | merge it, or finish the draft |
| `dropped` | A backport PR was closed unmerged, reported with whatever reason someone left on it | none if the reason still holds |
| `MISSING` | No backport PR ever existed and nothing on the branch matches | cherry-pick it by hand, or drop it deliberately |

`dropped` is usually healthy — a maintainer deciding a fix does not apply to that line, e.g. because the feature it repairs never shipped there. The recorded reason is printed so the next release does not re-open the same investigation. A `dropped` entry reading `no reason recorded` is the one that needs a human.

## Backports the labels do not account for

The verdicts start from labels, so on their own they cannot see a backport of a PR nobody labelled. Two further sections start from the other side: every PR on the release branch, and the originals it names by the bot's head branch or in a `Backport of` phrase (see [How landing is established](#how-landing-is-established)). The header carries their counts after a `|`, e.g. `backported=42 | DUPLICATE=3 unlabelled=13`.

`UNLABELLED` lists each original that backport PRs on the branch claim although it is not a candidate for the branch, with every backport PR claiming it and that PR's state. Each entry also says what those PRs amount to: `backport PR merged` once one of them merged, `claimed here, not merged` while the only live one is still open, and `claimed here, closed unmerged` when every one was closed. A merged claim is only a merged PR, since an unlabelled original is not checked commit by commit; it reads `backport PR merged, confirmed by @login` when the PR carries the [attestation marker](#attesting-a-backport-complete). Most are hand backports of PRs that never carried a label, merged on purpose. An original that does carry a backport request, one that resolved to a different line at merge time, is listed with its labels. The section is informational and never moves the exit code: the gate answers whether everything labelled landed, and an unlabelled backport can only add to a branch, never leave a labelled change off it.

`DUPLICATE` lists each original claimed by more than one live backport PR, labelled or not:

| Kind | Meaning | Action |
|------|---------|--------|
| `open-twice` | Two or more backport PRs are open for it — typically the bot's conflict draft next to a hand backport, or a fork PR next to its reopening from a branch in this repository | merge one, close the rest |
| `open-after-merge` | A backport already merged and another one is still open | close the leftover, or merge it if it is the second half of a split backport |

Both kinds make the audit exit `1`. Neither reaches a verdict, which judges the commits on the branch and names at most one open backport PR, and in both a human has to decide what happens to the open PR before the cut. A closed PR next to an open one is the normal shape of a redone backport and is not flagged, and neither are two merged ones, which are history the branch already carries.

```console
  DUPLICATE -- one original, more than one live backport PR (3):
    https://github.com/cozystack/cozystack/pull/3936
      #3936 fix(rabbitmq): right-size the default resources preset to s1.nano
      label=kind/backport -- a backport PR still open after another one merged
      backport #4372 OPEN (draft) https://github.com/cozystack/cozystack/pull/4372
      backport #4393 MERGED https://github.com/cozystack/cozystack/pull/4393

  UNLABELLED -- backport PRs here for originals not labelled for this line (13, informational):
    https://github.com/cozystack/cozystack/pull/4280
      #4280 fix(kafka): keep the pre-delete hook's credentials as long as its Job
      backport PR merged
      backport #4421 CLOSED https://github.com/cozystack/cozystack/pull/4421
      backport #4456 MERGED https://github.com/cozystack/cozystack/pull/4456
```

The titles of unlabelled originals are not in any listing the audit already makes, so they come from one GraphQL request covering the whole run. If it fails, the report goes out without them and says so on stderr; the URLs are derived locally and the verdicts and exit code do not depend on it.

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

Evidence is weighed per commit, because a commit is what a backport can lose:

1. **Reachability.** The PR's merge commit is reachable from the release branch, i.e. it merged before the branch was cut (or `main` was later merged in). Nothing was ever needed: `in-branch`.
2. **A linked backport PR.** Found by the bot's head branch `backport-<N>-to-release-X.Y`, or by a `Backport of #N` reference in the body, which is what a hand-written backport carries. It never outranks the commits (item 3): a merged backport PR with one of the PR's commits missing is `partial`, because the bot's conflict drafts stop at the first commit that does not apply and drop the rest, so a merged draft looks finished at PR level. `MERGED` settles it alone only for a PR of a single commit, as it always has — including on a `--no-fetch` run whose local refs predate the backport's merge. For a PR of several commits, none of them found and a merged backport PR is `unverified`. `OPEN` is `pending`; `CLOSED` is `dropped`.

   A backport carrying several changes links to every original its `Backport of` phrase names: a list (`Backport of #3938 and #4280`, `Backport of #1, #2, and #3`), and the `Backport of #4253 to release-1.6, together with #3460` form used for a dependency pulled along. Only that phrase is read, so the issue a backport fixes or a CI run it cites further on is never taken for an original. A list counts in full only when it visibly ends — at the end of the line or the sentence, or where the `to release-X.Y` clause starts. One that runs on into anything else (`Backport of #10, #20 is not included`) may be saying something about its later items, so only its first reference counts. A reference qualified with this repository (`cozystack/cozystack#N`, any case) counts like a bare `#N`, and one qualified with any other repository is skipped, because reading `other/repo#20` as local #20 would let an unrelated backport vouch for whichever local PR carries that number.
3. **The branch's own history, commit by commit.** Every commit the PR contributed has to be on the branch; merge commits and commits that change nothing are skipped, since the backport bot drops both. A commit counts when it is reachable, when an `-x` cherry-pick reference names it, or when a commit with its subject is there. Subjects are matched one to one: two commits with the same subject, `fix tests` say, need two branch commits of that name, and a branch commit whose `-x` reference names any other commit than the PR's merge commit is evidence only through that reference. Nothing weaker is taken: not an `-x` reference to the PR's merge commit, which survives a cherry-pick later amended to drop a commit, and not matching lines, which an unrelated line of the same text satisfies. A wrong `backported` is the one answer a release gate must not give, while a false alarm costs a look and can be settled by an attestation. All commits is `backported`, some is `partial`. This is also what catches a hand backport nobody linked.

### Attesting a backport complete

Some complete backports cannot be shown commit by commit, most often a fork contributor's backport squashed into one commit that carries only the first commit's subject. A maintainer who has checked such a backport can say so on the merged backport PR, with a comment that consists of this single line and nothing else:

```text
backport-audit: complete
```

That turns the candidate's `partial` or `unverified` into `confirmed`, which does not fail the gate; the report names the author, links the comment, and keeps listing the commits nothing on the branch names.

The marker is an explicit attestation by whoever wrote it, never something the audit infers. It counts only when it is the whole comment: exactly `backport-audit: complete`, in that case, starting at the beginning of its line, with nothing but blank lines before it and nothing but whitespace after it. Any other text makes the comment something other than an attestation, so a sentence such as "do not post backport-audit: complete yet", a quote or a code block never counts, and neither does the marker with an explanation next to it; post the explanation as a separate comment. It counts only on a backport PR that merged, since an open or closed one put nothing on the branch to vouch for. And it counts only from someone with a hand in the repository: the comment's GitHub author association has to be `OWNER`, `MEMBER` or `COLLABORATOR`, and comments from automation are ignored whatever their association — logins ending in `[bot]`, and the review and dependency bots gh reports without that suffix, such as `coderabbitai`, `dependabot`, `renovate` and `copilot`. A review bot quoting the marker, or a contributor vouching for their own backport, confirms nothing.

## Machine-readable output

```bash
# URLs of what never landed, ready to paste into a tracking issue
go run ./cmd/backport-audit --json release-1.5 | jq -r '.[].candidates[] | select(.status=="MISSING") | .url'

# every open backport PR the audit links, labelled or not, to merge or close, across lines
go run ./cmd/backport-audit --json release-1.6 release-1.5 \
  | jq -r '[.[] | (.candidates[], .unlabelled[]) | .backport_prs[] | select(.state=="OPEN") | .url] | unique[]'

# dropped items with the reason someone recorded
go run ./cmd/backport-audit --json release-1.4 \
  | jq -r '.[].candidates[] | select(.status=="dropped") | "#\(.number)\t\(.reason)"'

# commits a partial backport still lacks, one per line
go run ./cmd/backport-audit --json release-1.6 \
  | jq -r '.[].candidates[] | select(.status=="partial") | "#\(.number)\t\(.missing_commits[] | "\(.oid[0:9]) \(.subject)")"'

# open backport PRs competing for one original, to settle before the cut
go run ./cmd/backport-audit --json release-1.6 \
  | jq -r '.[].duplicates[] | "#\(.number) \(.kind): \([.backport_prs[] | select(.state=="OPEN") | .url] | join(" "))"'
```

Output is an object keyed by branch, each holding three arrays: `candidates`, `unlabelled` and `duplicates`, each always present and possibly empty. `partial`, `unverified` and `confirmed` candidates carry `missing_commits`, each an `oid` and a `subject`, and a `confirmed` one also carries `confirmation`: `by`, `url` and `backport_pr`. An `unlabelled` record carries the same `confirmation` when its merged backport PR has the marker. A `candidates` record:

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

An `unlabelled` record, where `labels` lists the backport requests the original carries for other lines and is empty when it carries none:

```json
{
  "number": 4280,
  "title": "fix(kafka): keep the pre-delete hook's credentials as long as its Job",
  "url": "https://github.com/cozystack/cozystack/pull/4280",
  "labels": [],
  "backport_prs": [
    {"number": 4421, "state": "CLOSED", "url": "..."},
    {"number": 4456, "state": "MERGED", "url": "..."}
  ]
}
```

A `duplicates` record, where `label` is the request the original was audited under on this line and is empty when it is not a candidate here:

```json
{
  "number": 4254,
  "title": "feat(kubevirt): expose migration configuration through platform values",
  "url": "https://github.com/cozystack/cozystack/pull/4254",
  "label": "kind/backport",
  "kind": "open-twice",
  "backport_prs": [
    {"number": 4322, "state": "OPEN", "url": "...", "draft": true},
    {"number": 4339, "state": "OPEN", "url": "..."},
    {"number": 4402, "state": "OPEN", "url": "..."}
  ]
}
```

## Limits

A `MISSING`, `partial` or `unverified` verdict is a prompt to check, not proof of absence. A hand backport squashed or reworded without `-x` reads as `partial` or `unverified` until someone attests it complete, and as `MISSING` when nothing links it to its original; confirm at diff level before redoing the work. And the audit reports *that* something is missing, never *why* — a run of `MISSING` entries clustered in time usually means the bot itself was failing during that window, which is worth checking before cherry-picking them one by one.

The audit reads labels as they stand when it runs. A label added afterwards is seen only by the next run, and until then its PR is either absent or, if someone already backported it, listed as `UNLABELLED` rather than audited, so run it right before the cut rather than once at the start of the day.

A label is always read against the release lines as they stood when the PR merged, however much later the label was added, because nothing the audit lists records when that happened. A PR merged on 2026-07-01 and labelled `kind/backport` a month later, after `v1.6.0` published, is audited as a `release-1.5` candidate. [`backport.yaml`](../../.github/workflows/backport.yaml) runs again when the label is added and resolves its targets at that moment, so it opens the backport against `release-1.6`. When the line has moved on between the merge and the label, the two therefore disagree: the audit expects the change on the merge-time line and reports it `MISSING` there unless it arrived some other way, and lists the bot's backport under `UNLABELLED` on the newer one. `TestCandidateLabel` pins the audit's side of this.

A backport PR that names its original neither by the bot's head branch nor in a `Backport of` phrase links to nothing, so it is invisible to both cross-checks: it is not listed as `UNLABELLED` and does not count towards a `DUPLICATE`. For a labelled original the branch history can still prove the change landed, through an identical commit subject or an `-x` cherry-pick reference. For an unlabelled one those are not read at all, so a backport of an unlabelled PR that only a cherry-pick trailer points to, or nothing does, appears nowhere in the report.
