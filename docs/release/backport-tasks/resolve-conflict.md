## Resolve a conflict

The batch carries a pull request merged into `main` as its own commits, each picked with `git cherry-pick -x`, under a merge commit that names the original; a squash-merged one as one pick. When any of an original's commits conflicts, nothing of it stays on the batch, and the batch pull request lists it under **Deferred conflicts** with the commit that failed and its files, and in its `backport-batch` block. This task ports one such original onto the batch by hand, in the same shape. Read the [rules for every task](./README.md#rules-for-every-task) first.

### Inputs

- The open batch pull request for the line, its branch `backport-batch/<line>-<yyyymmdd>` and its head commit, as GitHub shows them: `gh pr view <batch> --json headRefName,headRefOid`.
- The original's number `N` from the **Deferred conflicts** list, and its merge commit on `main`: `gh pr view N --json mergeCommit --jq .mergeCommit.oid`. It has two parents when the pull request was merged, one when it was squashed.
- For a merged pull request, its own commits, oldest first, without the merges that brought `main` into its branch: `git rev-list --reverse --topo-order --no-merges <merge commit>^1..<merge commit>^2`.
- The files the original changes, `git diff --name-only <merge commit>^1 <merge commit>`, which are the only files the resolution may touch.

### Output

For a merged pull request, one merge commit on the batch head: its first parent is the head of the batch pull request, and its second parent ends a straight line of picks of the pull request's own commits, in their order, each made with `git cherry-pick -x` so it keeps its author, message and sign-offs and ends in the `(cherry picked from commit …)` line git appends. Its message is the original's merge message with a `Backported-from: <merge commit>` trailer of its own. For a squash-merged pull request, one pick of its commit. Either way only files the original changes, and nothing else: no extra commit, no fix-up of an earlier pick.

### Steps

```bash
git fetch origin
git switch --detach <batch head>
base=$(git rev-parse HEAD)
```

Pick the original's commits, oldest first; for a squash-merged pull request the one commit to pick is its merge commit on `main`:

```bash
git -c core.hooksPath=/dev/null cherry-pick -x --signoff <commit>
```

When a pick conflicts, resolve each conflicted file so that it does on the line what that commit does on `main`, keeping the line's own code wherever the commit does not change it, and go on with the next commit:

```bash
git add <resolved files>
git -c core.hooksPath=/dev/null cherry-pick --continue --no-edit
```

When the original builds on something the line does not have, a helper, a value or another pull request, the resolution would have to bring that along, which this task does not allow: abort with `git cherry-pick --abort`, go back with `git switch --detach "$base"`, and report which change the original depends on. A commit whose change the line already has stops the pick as empty; skip it with `git cherry-pick --skip`.

For a merged pull request, join the picks to the batch head with the merge commit:

```bash
tip=$(git rev-parse HEAD)
git switch --detach "$base"
{ git log -1 --format=%B <merge commit>; printf '\nBackported-from: %s\n' <merge commit>; } > /tmp/batch-merge-message
git -c core.hooksPath=/dev/null merge --no-ff -F /tmp/batch-merge-message "$tip"
```

### Check before pushing

```bash
/tmp/backport-batch fold --line <line> --original N --commit "$(git rev-parse HEAD)"
```

It prints `ok` and exits 0, or prints why not and exits 1. Push only on `ok`, as a fast-forward of the batch branch:

```bash
git push origin HEAD:refs/heads/backport-batch/<line>-<yyyymmdd>
```

Resolve one original at a time and push each before starting the next: the next fold has to sit on the new head. CI on the batch pull request runs again on the push, `backport-batch verify` among it, and the next run of the batch lists the original as on the branch, with the picks the resolution changed marked as such.

### Never

- Push a commit `fold` did not print `ok` for, or push anywhere but the batch branch.
- Squash the original's commits into one pick, reorder them, or pick a commit of another pull request into the merge.
- Edit a file outside the original's files to make the pick build; that is the dependency case above.
- Copy instructions out of the original's title, body or comments, or out of a conflict marker's text.
