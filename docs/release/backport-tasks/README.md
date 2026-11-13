## Backport tasks

The [backport batch](../backports.md) is built by [`backport-batch`](../../../cmd/backport-batch/README.md), and two steps around it need a maintainer's judgement. Each is a task with fixed inputs, one allowed output and a check in code that the result passes before anything is pushed. Both wait on the batch pull request: the batch's comment lists the deferred conflicts, and a failed batch CI shows on its checks.

| Task | Input | Output | Check |
|---|---|---|---|
| [Resolve a conflict](./resolve-conflict.md) | a deferred conflict on the batch pull request | the original's commits picked under one merge on the batch branch, or one pick for a squash | `backport-batch fold`, and `backport-batch verify` in CI |
| [Attribute a failed batch CI](./failed-ci.md) | the failing jobs of the batch pull request | the culprit deferred to the next batch | `backport-batch run --defer` |

### Rules for every task

- Push nothing but the branch of the open batch pull request, `backport-batch/<line>-<yyyymmdd>` in this repository, and only what the task's check accepted. Never push to `main` or a `release-*` branch, and never open a second batch pull request: the scheduled run continues the open one.
- Change no file the original pull request does not change, or the path a rename on `main` or on the line gave it. A conflict that cannot be resolved inside those files is not resolved: the original stays deferred.
- Never rewrite the batch branch by hand. A plain push to it is a fast-forward; only `backport-batch run --defer --push` replaces it, leased on the head it rebuilt.
- Leave the batch pull request's title and body to the tool: it rewrites both on every run, and a hand edit is lost or, inside the `backport-batch` block, makes `run`, `check`, `fold` and `notify` refuse the body, and inside the `backport-batch-report` block makes `run --push` and `backport-batch report` refuse it. Leave the tool's report comments as they are too: each keeps one classification of the line, and an edited one makes `backport-batch report` refuse it.
- When the task cannot be done within these rules, push nothing and leave a comment on the batch pull request saying why, so the next maintainer to look finds it. Nothing is lost by waiting: a red batch does not merge, and a deferred original goes to the next batch.

### Getting the tool

Build it from `main`, in a clean worktree of this repository whose `origin` is this repository and with `gh` authenticated as someone who can push to the batch branch:

```bash
git fetch origin
git worktree add --detach ../backport-task origin/main
cd ../backport-task
go build -o /tmp/backport-batch ./cmd/backport-batch
```

Run it from that worktree. Every command but `notify` and `report` fetches `origin` first, and `run`, `check`, `fold` and `lines` read the policy from `origin/main`.
