# Contributing Conventions for AI Agents

Project-side conventions for commits, branches, and pull requests in Cozystack.

## Checklist for Creating a Pull Request

- [ ] Commit message follows Conventional Commits format
- [ ] Commit is signed off with `--signoff`
- [ ] Branch is rebased on `upstream/main` (no extra commits)
- [ ] PR body includes description and release note

## Commit Format

Follow [Conventional Commits](https://www.conventionalcommits.org/) with `--signoff`:

```bash
git commit --signoff -m "type(scope): brief description"
```

**Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore`

**Scopes:**
- System: `dashboard`, `platform`, `cilium`, `kube-ovn`, `linstor`, `fluxcd`, `cluster-api`
- Apps: `postgres`, `mariadb`, `redis`, `kafka`, `clickhouse`, `virtual-machine`, `kubernetes`
- Other: `api`, `hack`, `tests`, `ci`, `docs`

Breaking changes: append `!` after type/scope (`feat(api)!: ...`) or add a `BREAKING CHANGE:` footer.

**Examples:**
```bash
git commit --signoff -m "feat(dashboard): add config hash annotations to restart pods on config changes"
git commit --signoff -m "fix(postgres): update operator to version 1.2.3"
git commit --signoff -m "docs: add installation guide"
```

## Rebasing on upstream/main

If the branch has extra commits, clean it up:

```bash
git fetch upstream
git checkout -b my-feature upstream/main
git cherry-pick <your-commit-hash>
git push -f origin my-feature:my-branch-name
```

## Pull Request Body

Fill in the template at [`.github/PULL_REQUEST_TEMPLATE.md`](../../.github/PULL_REQUEST_TEMPLATE.md). It includes the required `release-note` block.

Create the PR with `gh pr create --title "type(scope): brief description" --body-file <file>`.

## Fetching Unresolved Review Comments

Cozystack uses GitHub review threads with resolution status. Only unresolved threads are actionable — resolved threads are already handled.

The REST endpoint `/pulls/{pr}/reviews` returns review summaries, not individual review comments. Use the GraphQL API to access `reviewThreads` with `isResolved` status:

```bash
gh api graphql -F owner=cozystack -F repo=cozystack -F pr=<PR_NUMBER> -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              id
              path
              line
              author { login }
              bodyText
              url
              createdAt
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .comments.nodes[]'
```

Compact one-line variant:

```bash
gh api graphql -F owner=cozystack -F repo=cozystack -F pr=<PR_NUMBER> -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          isResolved
          comments(first: 100) {
            nodes {
              path
              line
              author { login }
              bodyText
            }
          }
        }
      }
    }
  }
}' --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false) | .comments.nodes[] | "\(.path):\(.line // "N/A") - \(.author.login): \(.bodyText[:150])"'
```
