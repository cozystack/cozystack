# backport-batch

Builds the automatic backport batch for a release line and gates a patch cut on it. What goes into a batch is decided by the rules of the [backport policy](../../docs/release/backports.md) alone; this page describes how the tool applies them and does not restate them.

## Usage

```bash
go build ./cmd/backport-batch
./backport-batch run   --line release-1.7 [--onto SHA] [--date YYYY-MM-DD] [--push]
./backport-batch check --line release-1.7
./backport-batch fold  --line release-1.7 --original 4321 --commit SHA
```

Requires `git` and an authenticated `gh` on PATH, and a remote called `origin`. Every command fetches `origin` first. The batch runs only for the two newest `release-X.Y` lines.

Build the binary wherever an exit code matters: `go run` turns any non-zero exit into its own `1`, so under it a check that could not be completed looks the same as one that found something missing.

### run

Switches the current working tree to `backport-batch/<line>-<yyyymmdd>`, created at `origin/<line>`, and picks onto it with `git cherry-pick -x --signoff`, one original at a time in the order they merged into `main`, with `-m 1` for a merge commit. The tree has to be clean. A pick that conflicts is aborted and recorded with its files, and the run goes on with the next one. Signing follows your git configuration; hooks are off for the picks, so each commit carries the original message as git writes it.

The pull request body goes to stdout, built from the line's own `.github/PULL_REQUEST_TEMPLATE.md`: the originals on the branch, the deferred conflicts with their files, the skipped pull requests counted by rule with the lists collapsed, the originals' release notes, and a `backport-batch` block of JSON that lists the deferred conflicts for `check`. The title is `fix(backport): batch for <line>, <date>`. A body that would pass GitHub's 65536-byte limit leaves out its longest skipped lists, and the run prints them on stderr instead.

`--onto SHA` continues a batch already pushed instead of starting a new one. SHA has to be the head the remote has for a batch branch of the line right now, read with `git ls-remote`, so a head someone pushed onto since is refused. What that branch carries stays, and only what it does not carry yet is picked on top, so the next push is a fast-forward.

`--push` pushes the branch, refusing anything but a fast-forward of what the remote has, then opens the draft pull request against the line with `gh pr create --draft`, or updates the open batch pull request with `gh pr edit`. While a batch pull request is open on another branch it refuses, so a line never has two.

### check

Exits 1 and prints the URL of every pull request the rules take that is neither on the line nor on the open batch pull request, 0 when there is none, and 2 when it cannot tell. A pull request that the open batch lists as a deferred conflict prints as a warning and does not fail the check. Run it before cutting a patch release or an rc from a line.

### fold

Checks a hand-made resolution of a deferred conflict before it is pushed onto the batch. The commit has to have one parent, the head of the open batch pull request; to touch only files the original touched; to change something; and to name the original's merge commit, or one of its commits, in a `(cherry picked from commit …)` line, and nothing else. `git cherry-pick -x -m 1 <merge commit>` on the batch head, with the conflict resolved, produces exactly that. fold prints `ok` and exits 0, or prints why not and exits 1. It never commits or pushes; push one checked commit at a time, since the next one has to sit on the new head.

## Maintainer overrides

Every command reads the merged pull requests that carry `backport/include` or `backport/skip`, through GitHub search, which answers at most 1000 per label; a listing that long is an error rather than a complete answer. `backport/skip` keeps a pull request out whatever the rules say, and wins over `backport/include`, which lifts rules 2 to 4 and nothing else: what the line already carries and, on the older line, what the newer one does not carry yet still decide. The batch pull request lists every overridden original and what became of it. Adding a label takes triage access to the repository, which is the trust the labels rest on; who added one is not checked.

## What counts as already there

An original is on a line when any of these holds, and the batch does not pick it again:

- its merge commit is reachable from the line;
- a commit on the line names it, or one of its commits, by full SHA in a `(cherry picked from commit …)` line of its trailer block, where `git cherry-pick -x` writes it; the same line anywhere else in a message is prose and does not count;
- a commit on the line says `Backport of #N` for it, which is what a hand backport merged as one commit carries;
- picking it onto the line would change nothing.

An original is on the open batch pull request when a commit of that branch names it the same way. The same rules on the newer line decide rule 6.

Only the line's own commits count, the ones after the point where it branched from `main`, never a pull request against it: an open backport pull request, its title and its body are anyone's to write. The tool trusts the line's history because only maintainers merge into `release-*`; if that ever changes, a commit that names an original without carrying all of it would hide the rest. The batch pull request counts only when it comes from a `backport-batch/<line>-<yyyymmdd>` branch of this repository, not a fork, and was opened by GitHub Actions' bot or by someone whose permission on the repository GitHub reports as write or admin, and only at the head GitHub reports for it. The author association is not enough: an organization member or an invited collaborator can hold read access only. With more than one such pull request open the tool refuses to go on.

Titles and bodies are read from the merge commits on `main`, which hold them as a maintainer merged them; the live pull request stays editable by its author.

## What CI verifies

The tool does not build or test anything. CI on the batch pull request runs the line's own build and tests against what was picked, and a maintainer reviews and merges it. A deferred conflict waits for the next batch unless someone resolves it, checks the resolution with `fold` and pushes it, and then CI runs again.

## Untrusted text

Titles, release notes and file names in the body come from whoever opened the originals. A title, a reason or a file name goes into a code span whose fence no backtick inside can close, and the release notes go into the `release-note` block with every run of three backticks or tildes defused, so GitHub acts on no mention, reference, link or HTML in them. Inside that block, in case a note is copied out of it, links lose any target outside this repository, and closing keywords, cross-repository references and mentions have their `@`, `#` and `://` swapped for fullwidth signs, which act on nothing however the backticks around them pair up. Zero-width, bidi and control characters are dropped first, and everything the tool prints passes the same filter, so nothing it relays can drive a terminal. Commit messages are read through `git cat-file --batch`, which gives each object's length, and end at their first NUL as `git log` shows them, so a message cannot pass for a second commit or hide a trailer from a reader of the log. Every value handed to git comes after `--end-of-options` or `--`, refs pass `git check-ref-format`, SHAs have to be 40 hex digits, and nothing runs through a shell.
