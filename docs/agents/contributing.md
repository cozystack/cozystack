# Instructions for AI Agents

Guidelines for AI agents contributing to Cozystack.

## Checklist for Creating a Pull Request

- [ ] Changes are made and tested
- [ ] Commit message uses correct `[component]` prefix
- [ ] Commit is signed off with `--signoff`
- [ ] Branch is rebased on `upstream/main` (no extra commits)
- [ ] PR body includes description and release note
- [ ] PR is pushed and created with `gh pr create`

## How to Commit and Create Pull Requests

### 1. Make Your Changes

Edit the necessary files in the codebase.

### 2. Commit with Proper Format

Use the `[component]` prefix and `--signoff` flag:

```bash
git commit --signoff -m "[component] Brief description of changes"
```

**Component prefixes:**
- System: `[dashboard]`, `[platform]`, `[cilium]`, `[kube-ovn]`, `[linstor]`, `[fluxcd]`, `[cluster-api]`
- Apps: `[postgres]`, `[mysql]`, `[redis]`, `[kafka]`, `[clickhouse]`, `[virtual-machine]`, `[kubernetes]`
- Other: `[tests]`, `[ci]`, `[docs]`, `[maintenance]`

**Examples:**
```bash
git commit --signoff -m "[dashboard] Add config hash annotations to restart pods on config changes"
git commit --signoff -m "[postgres] Update operator to version 1.2.3"
git commit --signoff -m "[docs] Add installation guide"
```

### 3. Rebase on upstream/main (if needed)

If your branch has extra commits, clean it up:

```bash
# Fetch latest
git fetch upstream

# Create clean branch from upstream/main
git checkout -b my-feature upstream/main

# Cherry-pick only your commit
git cherry-pick <your-commit-hash>

# Force push to your branch
git push -f origin my-feature:my-branch-name
```

### 4. Push Your Branch

```bash
git push origin <branch-name>
```

### 5. Create Pull Request

Write the PR body to a temporary file:

```bash
cat > /tmp/pr_body.md << 'EOF'
## What this PR does

Brief description of the changes.

Changes:
- Change 1
- Change 2

### Release note

```release-note
[component] Description for changelog
```
EOF
```

Create the PR:

```bash
gh pr create --title "[component] Brief description" --body-file /tmp/pr_body.md
```

Clean up:

```bash
rm /tmp/pr_body.md
```

## Addressing AI Bot Reviewer Comments

When the user asks to fix comments from AI bot reviewers (like Qodo, Copilot, etc.):

### 1. Get PR Comments

View all comments on the pull request:

```bash
gh pr view <PR-number> --comments
```

Or for the current branch:

```bash
gh pr view --comments
```

### 2. Review Each Comment Carefully

**Important**: Do NOT blindly apply all suggestions. Each comment should be evaluated:

- **Consider context** - Does the suggestion make sense for this specific case?
- **Check project conventions** - Does it align with Cozystack patterns?
- **Evaluate impact** - Will this improve code quality or introduce issues?
- **Question validity** - AI bots can be wrong or miss context

**When to apply:**
- ✅ Legitimate bugs or security issues
- ✅ Clear improvements to code quality
- ✅ Better error handling or edge cases
- ✅ Conformance to project conventions

**When to skip:**
- ❌ Stylistic preferences that don't match project style
- ❌ Over-engineering simple code
- ❌ Changes that break existing patterns
- ❌ Suggestions that show misunderstanding of the code

### 3. Apply Valid Fixes

Make changes addressing the valid comments. Use your judgment.

### 4. Leave Changes Uncommitted

**Critical**: Do NOT commit or push the changes automatically.

Leave the changes in the working directory so the user can:
- Review the fixes
- Decide whether to commit them
- Make additional adjustments if needed

```bash
# After making changes, show status but DON'T commit
git status
git diff
```

The user will commit and push when ready.

## Code Review Comments

When asked to fix code review comments, **always work only with unresolved (open) comments**. Resolved comments should be ignored as they have already been addressed.

### Getting Unresolved Review Comments

Use GitHub GraphQL API to fetch only unresolved review comments from a pull request:

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

### Filtering for Unresolved Comments

The key filter is `select(.isResolved == false)` which ensures only unresolved review threads are processed. Each thread can contain multiple comments, but if the thread is resolved, all its comments should be ignored.

### Working with Review Comments

1. **Fetch unresolved comments** using the GraphQL query above
2. **Parse the results** to identify:
   - File path (`path`)
   - Line number (`line` or `originalLine`)
   - Comment text (`bodyText`)
   - Author (`author.login`)
3. **Address each unresolved comment** by:
   - Locating the relevant code section
   - Making the requested changes
   - Ensuring the fix addresses the concern raised
4. **Do NOT process resolved comments** - they have already been handled

### Example: Compact List of Unresolved Comments

For a quick overview of unresolved comments:

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

### Important Notes

- **REST API limitation**: The REST endpoint `/pulls/{pr}/reviews` returns review summaries, not individual review comments. Use GraphQL API for accessing `reviewThreads` with `isResolved` status.
- **Thread-based resolution**: Comments are organized in threads. If a thread is resolved (`isResolved: true`), ignore all comments in that thread.
- **Always filter**: Never process comments from resolved threads, even if they appear in the results.

### Example Workflow

```bash
# Get PR comments
gh pr view 1234 --comments

# Review comments and identify valid ones
# Make necessary changes to address valid comments
# ... edit files ...

# Show what was changed (but don't commit)
git status
git diff

# Tell the user what was fixed and what was skipped
```

## Git Permissions

Request these permissions when needed:
- `git_write` - For commit, rebase, cherry-pick, branch operations
- `network` - For push, fetch, pull operations

## Common Issues

**PR has extra commits?**  
→ Rebase on `upstream/main` and cherry-pick only your commits

**Wrong commit message?**  
→ `git commit --amend --signoff -m "[correct] message"` then `git push -f`

**Need to update PR?**  
→ `gh pr edit <number> --body "new description"`
