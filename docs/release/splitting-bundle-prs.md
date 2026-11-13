## Splitting a release-blocking bundle PR

Sometimes the work that has to land before a release is a 40-commit grab bag (CI stabilization, dependency bumps, chart fixes). Splitting it makes it reviewable and survivable. The strategy that has worked:

1. **Verify what's already on `main`.** Use `git merge-base --is-ancestor` and subject grep to drop commits that have already landed.
2. **Find existing open PRs that cover the same commits.** Force-push to the existing branch instead of opening a duplicate — preserves review threads.
3. **Identify the "bottleneck" PR.** The one whose changes make previously-silent misconfigurations into hard errors (typically an operator-version bump). Everything else can fan out in parallel.
4. **Squash iterative same-author bug-on-bug fixes** inside each split. Preserve authorship via `--author` on the squashed commit.
5. **Mark the keystone PR as draft** and link in the body to the dependency PRs. Reviewers know it intentionally fails CI until the chain lands.
6. **Beware of concurrent registry pushes.** When fanning out 5+ PR CI runs simultaneously, multiple `make image` jobs can collide on shared floating tags. The `IMAGE_TAG=pr-<N>-<sha>` refactor solved this; if you see `Conflicted with another upload of the same manifest`, `gh run rerun --failed`.
