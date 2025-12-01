# Changelog Generation Instructions

This file contains detailed instructions for AI-powered IDE on how to generate changelogs for Cozystack releases.

## When to use these instructions

Follow these instructions when the user explicitly asks to generate a changelog.

## Required Tools

Before generating changelogs, ensure you have access to `gh` (GitHub CLI) tool, which is used to fetch commit and PR author information. The GitHub CLI is used to correctly identify PR authors from commits and pull requests.

## Changelog Generation Process

When the user asks to generate a changelog, follow these steps in the specified order:

**CHECKLIST - All actions that must be completed:**
- [ ] Step 1: Update information from remote (git fetch)
- [ ] Step 2: Check current branch (must be main)
- [ ] Step 3: Determine release type and previous version (minor vs patch release)
- [ ] Step 4: Determine versions and analyze existing changelogs
- [ ] Step 5: Get the list of commits for the release period
- [ ] Step 6: Check additional repositories (website is REQUIRED, optional repos if tags exist)
  - [ ] **MANDATORY**: Check website repository for documentation changes WITH authors and PR links via GitHub CLI
  - [ ] **MANDATORY**: Check ALL optional repositories (talm, boot-to-talos, cozypkg, cozy-proxy) for tags during release period
  - [ ] **MANDATORY**: For ALL commits from additional repos, get GitHub username via CLI, prioritizing PR author over commit author.
- [ ] Step 7: Analyze commits (extract PR numbers, authors, user impact)
  - [ ] **MANDATORY**: For EVERY PR in main repo, get PR author via `gh pr view <PR_NUMBER> --json author --jq .author.login` (do NOT skip this step)
  - [ ] **MANDATORY**: Extract PR numbers from commit messages, then use `gh pr view` for each PR to get the PR author. Do NOT use commit author. Only for commits without PR numbers (rare), fall back to `gh api repos/cozystack/cozystack/commits/<hash> --jq '.author.login'`
- [ ] Step 8: Form new changelog (structure, format, generate contributors list)
- [ ] Step 9: Verify completeness and save

### 1. Updating information from remote

```bash
git fetch --tags --force --prune
```

This is necessary to get up-to-date information about tags and commits from the remote repository.

### 2. Checking current branch

Make sure we are on the `main` branch:

```bash
git branch --show-current
```

### 3. Determining release type and previous version

**Important**: Determine if you're generating a changelog for a **minor release** (vX.Y.0) or a **patch release** (vX.Y.Z where Z > 0).

**For minor releases (vX.Y.0):**
- Each minor version lives and evolves in its own branch (`release-X.Y`)
- You MUST compare with the **previous minor version** (v(X-1).Y.0), not the last patch release
- This ensures you capture all changes from the entire minor version cycle, including all patch releases
- Example: For v0.38.0, compare with v0.37.0 (not v0.37.8)
- Run a separate cycle to check the diff with the zero version of the previous minor release

**For patch releases (vX.Y.Z where Z > 0):**
- Compare with the previous patch version (vX.Y.(Z-1))
- Example: For v0.37.2, compare with v0.37.1

### 4. Determining versions and analyzing existing changelogs

**Determine the last published version:**
1. Get the list of version tags:
   ```bash
   git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V
   ```

2. Get the last tag:
   ```bash
   git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | sort -V | tail -1
   ```

3. Compare tags with existing changelog files in `docs/changelogs/` to determine the last published version (the newest file `vX.Y.Z.md`)

**Study existing changelog format:**
- Review recent changelog files to understand the format and structure
- Pay attention to:
  - **Feature Highlights format** (for minor releases): Use `## Feature Highlights` with `### Feature Name` subsections containing detailed descriptions (2-4 paragraphs each). See v0.35.0 and v0.36.0 for examples.
  - Section structure (Major Features and Improvements, Security, Fixes, Dependencies, etc.)
  - PR link format (e.g., `[**@username**](https://github.com/username) in #1234`)
  - Change description style
  - Presence of Breaking changes sections, etc.

### 5. Getting the list of commits

**Important**: Determine if you're generating a changelog for a **minor release** (vX.Y.0) or a **patch release** (vX.Y.Z where Z > 0).

**For patch releases (vX.Y.Z where Z > 0):**
Get the list of commits starting from the previous patch version to HEAD:

**⚠️ CRITICAL: Do NOT use --first-parent flag! It will skip merge commits including backports!**

```bash
# Get all commits including merge commits (backports)
git log <previous_version>..HEAD --pretty=format:"%h - %s (%an, %ar)"
```

For example, if generating changelog for `v0.37.2`:
```bash
git log v0.37.1..HEAD --pretty=format:"%h - %s (%an, %ar)"
```

**⚠️ IMPORTANT: Check for backports:**
- Look for commits with "[Backport release-X.Y]" in the commit message
- For backport PRs, find the original PR number mentioned in the backport commit message or PR description
- Use the original PR author (not the backport PR author) when creating changelog entries
- Include both the original PR number and backport PR number in the changelog entry (e.g., `#1606, #1609`)

**For minor releases (vX.Y.0):**
Minor releases must include **all changes** from patch releases of the previous minor version. Get commits from the previous minor release:

**⚠️ CRITICAL: Do NOT use --first-parent flag! It will skip merge commits including backports!**

```bash
# For v0.38.0, get all commits since v0.37.0 (including all patch releases v0.37.1, v0.37.2, etc.)
git log v<previous_minor_version>..HEAD --pretty=format:"%h - %s (%an, %ar)"
```

For example, if generating changelog for `v0.38.0`:
```bash
git log v0.37.0..HEAD --pretty=format:"%h - %s (%an, %ar)"
```

This will include all commits from v0.37.1, v0.37.2, v0.37.3, etc., up to v0.38.0.

**⚠️ IMPORTANT: Always check merge commits:**
- Merge commits may contain backports that need to be included
- Check all commits in the range, including merge commits
- For backports, always find and reference the original PR

### 6. Analyzing additional repositories

**⚠️ CRITICAL: This step is MANDATORY and must NOT be skipped!**

Cozystack release may include changes from related repositories. Check and include commits from these repositories if tags were released during the release period:

**Required repositories:**
- **Documentation**: [https://github.com/cozystack/website](https://github.com/cozystack/website)
  - **MANDATORY**: Always check this repository for documentation changes during the release period
  - **MANDATORY**: Get GitHub username for EVERY commit. Extract PR number from commit message, then use `gh pr view <PR_NUMBER> --repo cozystack/website --json author --jq .author.login` to get PR author. Only if no PR number, fall back to `gh api repos/cozystack/website/commits/<hash> --jq '.author.login'`

**Optional repositories (MUST check ALL of them for tags during release period):**
- [https://github.com/cozystack/talm](https://github.com/cozystack/talm)
- [https://github.com/cozystack/boot-to-talos](https://github.com/cozystack/boot-to-talos)
- [https://github.com/cozystack/cozypkg](https://github.com/cozystack/cozypkg)
- [https://github.com/cozystack/cozy-proxy](https://github.com/cozystack/cozy-proxy)

**⚠️ IMPORTANT**: You MUST check ALL optional repositories for tags created during the release period. Do NOT skip this step even if you think there might not be any tags. Use the process below to verify.

**Process for each repository:**

1. **Get release period dates:**
   ```bash
   # Get dates for the release period
   cd /path/to/cozystack
   RELEASE_START=$(git log -1 --format=%ai v<previous_version>)
   RELEASE_END=$(git log -1 --format=%ai HEAD)
   ```

2. **Check for commits in website repository (always required):**
   ```bash
   # Ensure website repository is cloned and up-to-date
   mkdir -p _repos
   if [ ! -d "_repos/website" ]; then
     cd _repos && git clone https://github.com/cozystack/website.git && cd ..
   fi
   cd _repos/website
   git fetch --all --tags --force
   git checkout main 2>/dev/null || git checkout master
   git pull
   
   # Get commits between release dates (with some buffer)
   git log --since="$RELEASE_START" --until="$RELEASE_END" --format="%H|%s|%an" | while IFS='|' read -r commit_hash subject author_name; do
     # Extract PR number from commit message
     PR_NUMBER=$(git log -1 --format="%B" "$commit_hash" | grep -oE '#[0-9]+' | head -1 | tr -d '#')
     
     # ALWAYS use PR author if PR number found, not commit author
     if [ -n "$PR_NUMBER" ]; then
       GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --repo cozystack/website --json author --jq '.author.login // empty' 2>/dev/null)
       echo "$commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/website#$PR_NUMBER"
     else
       # Only fallback to commit author if no PR number found (rare)
       GITHUB_USERNAME=$(gh api repos/cozystack/website/commits/$commit_hash --jq '.author.login // empty')
       echo "$commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/website@${commit_hash:0:7}"
     fi
   done
   
   # Look for documentation updates, new pages, or significant content changes
   # Include these in the "Documentation" section of the changelog WITH authors and PR links
   ```

3. **For optional repositories, check if tags exist during release period:**

   **⚠️ MANDATORY: You MUST check ALL optional repositories (talm, boot-to-talos, cozypkg, cozy-proxy). Do NOT skip any repository!**

   **Use the helper script:**
   ```bash
   # Get release period dates
   RELEASE_START=$(git log -1 --format=%ai v<previous_version>)
   RELEASE_END=$(git log -1 --format=%ai HEAD)
   
   # Run the script to check all optional repositories
   ./docs/changelogs/hack/check-optional-repos.sh "$RELEASE_START" "$RELEASE_END"
   ```
   
   The script will:
   - Check ALL optional repositories (talm, boot-to-talos, cozypkg, cozy-proxy)
   - Look for tags created during the release period
   - Get commits between tags (if tags exist) or by date range (if no tags)
   - Extract PR numbers from commit messages
   - For EVERY commit with PR number, get PR author via CLI: `gh pr view <PR_NUMBER> --repo cozystack/<repo> --json author --jq .author.login` (ALWAYS use PR author, not commit author)
   - For commits without PR numbers (rare), fallback to: `gh api repos/cozystack/<repo>/commits/<hash> --jq '.author.login'`
   - Output results in format: `commit_hash|subject|author_name|github_username|cozystack/repo#PR_NUMBER` or `cozystack/repo@commit_hash`

4. **Extract PR numbers and authors using GitHub CLI:**
   - **ALWAYS use PR author, not commit author** for commits from additional repositories
   - For each commit, extract PR number from commit message first: Extract `#123` pattern from commit message
   - If PR number found, use `gh pr view <PR_NUMBER> --repo cozystack/<repo> --json author --jq .author.login` to get PR author (the person who wrote the code)
   - Only if no PR number found (rare), fallback to commit author: `gh api repos/cozystack/<repo>/commits/<hash> --jq '.author.login'`
   - **Prefer PR numbers**: Use format `cozystack/website#123` if PR number found in commit message
   - **Fallback to commit hash**: Use format `cozystack/website@abc1234` if no PR number
   - **ALWAYS include author**: Every entry from additional repositories MUST include author in format `([**@username**](https://github.com/username) in cozystack/repo#123)`
   - Determine user impact and categorize appropriately
   - Format entries with repository prefix: `[website]`, `[talm]`, etc.

**Example entry format for additional repositories:**
```markdown
# If PR number found in commit message (REQUIRED format):
* **[website] Update installation documentation**: Improved installation guide with new examples ([**@username**](https://github.com/username) in cozystack/website#123).

# If no PR number (fallback, use commit hash):
* **[website] Update installation documentation**: Improved installation guide with new examples ([**@username**](https://github.com/username) in cozystack/website@abc1234).

# For optional repositories:
* **[talm] Add new feature**: Description of the change ([**@username**](https://github.com/username) in cozystack/talm#456).
```

**CRITICAL**: 
- **ALWAYS include author** for every entry from additional repositories
- **ALWAYS include PR link or commit hash** for every entry
- Never add entries without author and PR/commit reference
- **ALWAYS use PR author, not commit author**: Extract PR number from commit message, then use `gh pr view <PR_NUMBER> --repo cozystack/<repo> --json author --jq .author.login` to get the PR author (the person who wrote the code)
- Only if no PR number found (rare), fallback to commit author: `gh api repos/cozystack/<repo>/commits/<hash> --jq '.author.login'`
- The commit author (especially for squash/merge commits) is usually the person who merged the PR, not the person who wrote the code

### 7. Analyzing commits and PRs

**⚠️ CRITICAL: You MUST get the author from PR, not from commit! Always use `gh pr view` to get the PR author. Do NOT use commit author!**

**Get all PR numbers from commits:**
**⚠️ CRITICAL: Do NOT use --no-merges flag! It will skip merge commits including backports!**

```bash
# Extract all PR numbers from commit messages in the release range (including merge commits)
git log <previous_version>..<new_version> --format="%s%n%b" | grep -oE '#[0-9]+' | sort -u | tr -d '#'
```

**⚠️ IMPORTANT: Handle backports correctly:**
- Backport PRs have format: `[Backport release-X.Y] <original title> (#BACKPORT_PR_NUMBER)`
- The backport commit message or PR description usually mentions the original PR number
- For backport entries in changelog, use the original PR author (not the backport PR author)
- Include both original and backport PR numbers in the changelog entry (e.g., `#1606, #1609`)
- To find original PR from backport: Check the backport PR description or commit message for "Backport of #ORIGINAL_PR"

**For each PR number, get the author:**
   
   **CRITICAL**: The commit author (especially for squash/merge commits) is usually the person who merged the PR (or GitHub bot), NOT the person who wrote the code. **ALWAYS use the PR author**, not the commit author.
   
   **⚠️ MANDATORY: ALWAYS use `gh pr view` to get the PR author. Do NOT use commit author!**
   
   **ALWAYS use GitHub CLI** to get the PR author:
   
   ```bash
   # Usage: Get PR author - MANDATORY for EVERY PR
   # Loop through ALL PR numbers and get PR author (including backports)
   git log <previous_version>..<new_version> --format="%s%n%b" | grep -oE '#[0-9]+' | sort -u | tr -d '#' | while read PR_NUMBER; do
     # Check if this is a backport PR
     BACKPORT_INFO=$(gh pr view "$PR_NUMBER" --json body --jq '.body' 2>/dev/null | grep -i "backport of #" || echo "")
     if [ -n "$BACKPORT_INFO" ]; then
       # Extract original PR number from backport description
       ORIGINAL_PR=$(echo "$BACKPORT_INFO" | grep -oE 'backport of #([0-9]+)' | grep -oE '[0-9]+' | head -1)
       if [ -n "$ORIGINAL_PR" ]; then
         # Use original PR author
         GITHUB_USERNAME=$(gh pr view "$ORIGINAL_PR" --json author --jq '.author.login // empty')
         PR_TITLE=$(gh pr view "$ORIGINAL_PR" --json title --jq '.title // empty')
         echo "$PR_NUMBER|$ORIGINAL_PR|$GITHUB_USERNAME|$PR_TITLE|BACKPORT"
       else
         # Fallback to backport PR author if original not found
         GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --json author --jq '.author.login // empty')
         PR_TITLE=$(gh pr view "$PR_NUMBER" --json title --jq '.title // empty')
         echo "$PR_NUMBER||$GITHUB_USERNAME|$PR_TITLE|BACKPORT"
       fi
     else
       # Regular PR
       GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --json author --jq '.author.login // empty')
       PR_TITLE=$(gh pr view "$PR_NUMBER" --json title --jq '.title // empty')
       echo "$PR_NUMBER||$GITHUB_USERNAME|$PR_TITLE|REGULAR"
     fi
   done
   ```
   
   **⚠️ IMPORTANT**: You must run this for EVERY PR in the release period. Do NOT skip any PRs or assume the GitHub username based on the git author name.
   
   **CRITICAL**: Always use `gh pr view <PR_NUMBER> --json author --jq .author.login` to get the PR author. This correctly identifies the person who wrote the code, not the person who merged it (which is especially important for squash merges).
   
   **Why this matters**: Using the wrong author in changelogs gives incorrect credit and can confuse contributors. The merge/squash commit is created by the person who clicks "Merge" in GitHub, not the PR author.
   
**For commits without PR numbers (rare):**
- Only if a commit has no PR number, fall back to commit author: `gh api repos/cozystack/cozystack/commits/<hash> --jq '.author.login'`
- But this should be very rare - most commits should have PR numbers

**Extract PR number from commit messages:**
- Check commit message subject (`%s`) and body (`%b`) for PR references: `#1234` or `(#1234)`
- **Primary method**: Extract from commit message format `(#PR_NUMBER)` or `in #PR_NUMBER` or `Merge pull request #1234`
- Use regex: `grep -oE '#[0-9]+'` to find all PR numbers

**⚠️ CRITICAL: Verify PR numbers match commit messages!**
- Always verify that the PR number in the changelog matches the PR number in the commit message
- Common mistake: Using wrong PR number (e.g., #1614 instead of #1617) when multiple similar commits exist
- To verify: Check the actual commit message: `git log <commit_hash> -1 --format="%s%n%b" | grep -oE '#[0-9]+'`
- If multiple PR numbers appear in a commit, use the one that matches the PR title/description
- For merge commits, check the merged branch commits, not just the merge commit message

3. **Understand the change:**
   ```bash
   # Get PR details (preferred method)
   gh pr view <PR_NUMBER> --json title,body,url
   
   # Or get commit details if no PR number
   git show <commit_hash> --stat
   git show <commit_hash>
   ```
   - Review PR description and changed files
   - Understand functionality added/changed/fixed
   - **Determine user impact**: What can users do now? What problems are fixed? What improvements do users experience?

4. **For release branches (backports):**
   - If commit is from `release-X.Y` branch, check if it's a backport
   - Find original commit in `main` to get correct PR number:
     ```bash
     git log origin/main --grep="<part of commit message>" --oneline
     ```

### 8. Forming a new changelog

Create a new changelog file in the format matching previous versions:

1. **Determine the release type:**
   - **Minor release (vX.Y.0)** - use full format with **Feature Highlights** section. **Must include all changes from patch releases of the previous minor version** (e.g., v0.38.0 should include changes from v0.37.1, v0.37.2, v0.37.3, etc.)
   - **Patch release (vX.Y.Z, where Z > 0)** - use more compact format, includes only changes since the previous patch release

   **Feature Highlights format for minor releases:**
   - Use section header: `## Feature Highlights`
   - Include 3-6 major features as subsections with `### Feature Name` headers
   - Each feature subsection should contain:
     - **Detailed description** (2-4 paragraphs) explaining:
       - What the feature is and what problem it solves
       - How it works and what users can do with it
       - How to use it (if applicable)
       - Benefits and impact for users
     - **Links to documentation** when available (use markdown links)
     - **Code examples or configuration snippets** if helpful
   - Focus on user value and practical implications, not just technical details
   - Each feature should be substantial enough to warrant its own subsection
   - Order features by importance/impact (most important first)
   - Example format:
     ```markdown
     ## Feature Highlights
     
     ### Feature Name
     
     Detailed description paragraph explaining what the feature is...
     
     Another paragraph explaining how it works and what users can do...
     
     Learn more in the [documentation](https://cozystack.io/docs/...).
     ```

   **Important for minor releases**: After collecting all commits, **systematically verify** that all PRs from patch releases are included:
   ```bash
   # Extract all PR numbers from patch release changelogs
   grep -h "#[0-9]\+" docs/changelogs/v<previous_minor>.*.md | sort -u
   
   # Extract all PR numbers from the new minor release changelog
   grep -h "#[0-9]\+" docs/changelogs/v<new_minor>.0.md | sort -u
   
   # Compare and identify missing PRs
   # Ensure every PR from patch releases appears in the minor release changelog
   ```

2. **Structure changes by categories:**
   
   **For minor releases (vX.Y.0):**
   - **Feature Highlights** (required) - see format above
   - **Major Features and Improvements** - detailed list of all major features and improvements
   - **Improvements (minor)** - smaller improvements and enhancements
   - **Bug fixes** - all bug fixes
   - **Security** - security-related changes
   - **Dependencies & version updates** - dependency updates
   - **System Configuration** - system-level configuration changes
   - **Development, Testing, and CI/CD** - development and testing improvements
   - **Documentation** (include changes from website repository here - **MUST include authors and PR links for all entries**)
   - **Breaking changes & upgrade notes** (if any)
   - **Refactors & chores** (if any)
   
   **For patch releases (vX.Y.Z where Z > 0):**
   - **Features and Improvements** - new features and improvements
   - **Fixes** - bug fixes
   - **Security** - security-related changes
   - **Dependencies** - dependency updates
   - **System Configuration** - system-level configuration changes
   - **Development, Testing, and CI/CD** - development and testing improvements
   - **Documentation** (include changes from website repository here - **MUST include authors and PR links for all entries**)
   - **Migration and Upgrades** (if applicable)
   
   **Note**: When including changes from additional repositories, group them logically with main repository changes, or create separate subsections if there are many changes from a specific repository.

3. **Entry format:**
   - Use the format: `* **Brief description**: detailed description ([**@username**](https://github.com/username) in #PR_NUMBER)`
   - **CRITICAL - Get authorship correctly**: 
     - **ALWAYS use PR author, not commit author**: Extract PR number from commit message, then use `gh pr view` to get the PR author. The commit author (especially for squash/merge commits) is usually the person who merged the PR (or GitHub bot), NOT the person who wrote the code.
       ```bash
       # Get PR author from GitHub CLI (correct method)
       # Step 1: Extract PR number from commit message
       PR_NUMBER=$(git log <commit_hash> -1 --format="%s%n%b" | grep -oE '#[0-9]+' | head -1 | tr -d '#')
       
       # Step 2: Get PR author (the person who wrote the code)
       if [ -n "$PR_NUMBER" ]; then
         GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --json author --jq '.author.login')
       else
         # Only fallback to commit author if no PR number found (rare)
         GITHUB_USERNAME=$(gh api repos/cozystack/cozystack/commits/<commit_hash> --jq '.author.login')
       fi
       ```
       **Example**: For PR #1507, the squash commit has author "kvaps" (who merged), but the PR author is "lllamnyp" (who wrote the code). Using `gh pr view 1507 --json author --jq .author.login` correctly returns "lllamnyp".
     - **For regular commits**: Use the commit author directly:
       ```bash
       git log <commit_hash> -1 --format="%an|%ae"
       ```
     - **Validation**: Before adding to changelog, verify the author by checking:
       - For merge commits: Compare merge commit author vs PR author (they should be different)
       - Check existing changelogs for author name to GitHub username mappings
       - Verify with: `git log <merge_commit>^1..<merge_commit>^2 --format="%an" --no-merges`
   - **Map author name to GitHub username**: Check existing changelogs for author name mappings, or extract from PR links in commit messages
   - **Always include user impact**: Each entry must explain how the change affects users
     - For new features: explain what users can now do
     - For bug fixes: explain what problem is solved for users
     - For improvements: explain what users will experience better
     - For breaking changes: clearly state what users need to do
   - Group related changes
   - Use bold font for important components/modules
   - Focus on user value, not just technical details

4. **Add a link to the full changelog:**
   
   **For patch releases (vX.Y.Z where Z > 0):**
   ```markdown
   **Full Changelog**: https://github.com/cozystack/cozystack/compare/v<previous_patch_version>...v<new_version>
   ```
   Example: For v0.37.2, use `v0.37.1...v0.37.2`
   
   **For minor releases (vX.Y.0):**
   ```markdown
   **Full Changelog**: https://github.com/cozystack/cozystack/compare/v<previous_minor_version>...v<new_version>
   ```
   Example: For v0.38.0, use `v0.37.0...v0.38.0` (NOT `v0.37.8...v0.38.0`)
   
   **Important**: Minor releases must reference the previous minor release (vX.Y.0), not the last patch release, to include all changes from the entire minor version cycle.

5. **Generate contributors list:**

   **⚠️ SIMPLIFIED APPROACH: Extract contributors from the generated changelog itself!**
   
   Since you've already generated the changelog with all PR authors correctly identified, simply extract GitHub usernames from the changelog entries:
   
   ```bash
   # Extract all GitHub usernames from the current release changelog
   # This method is simpler and more reliable than extracting from git history
   
   # For patch releases: extract from the current changelog file
   grep -oE '\[@[a-zA-Z0-9_-]+\]' docs/changelogs/v<version>.md | \
     sed 's/\[@/@/' | sed 's/\]//' | \
     sort -u
   
   # For minor releases: extract from the current changelog file
   grep -oE '\[@[a-zA-Z0-9_-]+\]' docs/changelogs/v<version>.md | \
     sed 's/\[@/@/' | sed 's/\]//' | \
     sort -u
   ```
   
   **Get all previous contributors (to identify new ones):**
   ```bash
   # Extract GitHub usernames from all previous changelogs
   grep -hE '\[@[a-zA-Z0-9_-]+\]' docs/changelogs/v*.md | \
     grep -oE '@[a-zA-Z0-9_-]+' | \
     sort -u > /tmp/previous_contributors.txt
   ```
   
   **Identify new contributors (first-time contributors):**
   ```bash
   # Get current release contributors from the changelog
   grep -oE '@[a-zA-Z0-9_-]+' docs/changelogs/v<version>.md | \
     sort -u > /tmp/current_contributors.txt
   
   # Get all previous contributors
   grep -hE '@[a-zA-Z0-9_-]+' docs/changelogs/v*.md | \
     grep -oE '@[a-zA-Z0-9_-]+' | \
     sort -u > /tmp/all_previous_contributors.txt
   
   # Find new contributors (those in current but not in previous)
   comm -23 <(sort /tmp/current_contributors.txt) <(sort /tmp/all_previous_contributors.txt)
   ```
   
   **Why this approach is better:**
   - ✅ Uses the already-verified PR authors from the changelog (no need to query GitHub API again)
   - ✅ Automatically handles backports correctly (original PR authors are already in the changelog)
   - ✅ Simpler and faster (no git log parsing or API calls)
   - ✅ More reliable (matches exactly what's in the changelog)
   - ✅ Works for both patch and minor releases
   
   **Add contributors section to changelog:**
   
   Place the contributors section at the end of the changelog, before the "Full Changelog" link:
   ```markdown
   ## Contributors
   
   We'd like to thank all contributors who made this release possible:
   
   * [**@username1**](https://github.com/username1)
   * [**@username2**](https://github.com/username2)
   * [**@username3**](https://github.com/username3)
   * ...
   
   ### New Contributors
   
   We're excited to welcome our first-time contributors:
   
   * [**@newuser1**](https://github.com/newuser1) - First contribution!
   * [**@newuser2**](https://github.com/newuser2) - First contribution!
   ```
   
   **Formatting guidelines:**
   - List contributors in alphabetical order by GitHub username
   - Use the format: `* [**@username**](https://github.com/username)`
   - For new contributors, add " - First contribution!" note
   - If GitHub username cannot be determined, you can skip that contributor or use their git author name
   
   **When to include:**
   - **For patch releases**: Contributors section is optional, but can be included for significant releases
   - **For minor releases (vX.Y.0)**: Contributors section is required - you must generate and include the contributors list
   - Always verify GitHub usernames by checking commit messages, PR links in changelog entries, or by examining PR details

6. **Add a comment with a link to the GitHub release:**
   ```markdown
   <!--
   https://github.com/cozystack/cozystack/releases/tag/v<new_version>
   -->
   ```

### 9. Verification and saving

**Before saving, verify completeness:**

**For ALL releases:**
- [ ] Step 5 completed: **ALL commits included** (including merge commits and backports) - do not skip any commits
- [ ] Step 5 completed: **Backports identified and handled correctly** - original PR author used, both original and backport PR numbers included
- [ ] Step 6 completed: Website repository checked for documentation changes WITH authors and PR links via GitHub CLI
- [ ] Step 6 completed: **ALL** optional repositories (talm, boot-to-talos, cozypkg, cozy-proxy) checked for tags during release period
- [ ] Step 6 completed: For ALL commits from additional repos, GitHub username obtained via GitHub CLI (not skipped). For commits with PR numbers, PR author used via `gh pr view` (not commit author)
- [ ] Step 7 completed: For EVERY PR in main repo (including backports), PR author obtained via `gh pr view <PR_NUMBER> --json author --jq .author.login` (not skipped or assumed). Commit author NOT used - always use PR author
- [ ] Step 7 completed: **Backports verified** - for each backport PR, original PR found and original PR author used in changelog
- [ ] Step 8 completed: Contributors list generated
- [ ] All commits from main repository included (including merge commits)
- [ ] User impact described for each change
- [ ] Format matches existing changelogs

**For patch releases:**
- [ ] All commits from the release period are included (including merge commits with backports)
- [ ] PR numbers match commit messages
- [ ] Backports are properly identified and linked to original PRs

**For minor releases (vX.Y.0):**
- [ ] All changes from patch releases (vX.Y.1, vX.Y.2, etc.) are included
- [ ] Contributors section is present and complete
- [ ] Full Changelog link references previous minor version (vX.Y.0), not last patch
- [ ] Verify all PRs from patch releases are included:
  ```bash
  # Extract and compare PR numbers
  PATCH_PRS=$(grep -hE "#[0-9]+" docs/changelogs/v<previous_minor>.*.md | grep -oE "#[0-9]+" | sort -u)
  MINOR_PRS=$(grep -hE "#[0-9]+" docs/changelogs/v<new_minor>.0.md | grep -oE "#[0-9]+" | sort -u)
  MISSING=$(comm -23 <(echo "$PATCH_PRS") <(echo "$MINOR_PRS"))
  
  if [ -n "$MISSING" ]; then
    echo "Missing PRs from patch releases:"
    echo "$MISSING"
    # For each missing PR, check if it's a backport and verify change is included by description
  fi
  ```

**Only proceed to save after all checkboxes are verified!**

**Save the changelog:**
Save the changelog to file `docs/changelogs/v<version>.md` according to the version for which the changelog is being generated.

### Important notes

- **After fetch with --force** local tags are up-to-date, use them for work
- **For release branches** always check original commits in `main` to get correct PR numbers
- **Preserve the format** of existing changelog files
- **Group related changes** logically
- **Be accurate** in describing changes, based on actual commit diffs
- **Check for PR numbers** and commit authors
- **CRITICAL - Get authorship from PR, not from commit**: 
  - **ALWAYS use PR author**: Extract PR number from commit message, then use `gh pr view <PR_NUMBER> --json author --jq .author.login` to get the PR author
  - Do NOT use commit author - the commit author (especially for squash/merge commits) is usually the person who merged the PR, not the person who wrote the code
  - For commits without PR numbers (rare), fall back to commit author: `gh api repos/cozystack/cozystack/commits/<commit_hash> --jq '.author.login'`
  - **Workflow**: Extract PR numbers from commits → Use `gh pr view` for each PR → Get PR author (the person who wrote the code)
  - Example: For PR #1507, the commit author is `@kvaps` (who merged), but `gh pr view 1507 --json author --jq .author.login` correctly returns `@lllamnyp` (who wrote the code)
  - Check existing changelogs for author name to GitHub username mappings
  - **Validation**: Before adding to changelog, always verify the author using `gh pr view` - never use commit author for PRs
-  **MANDATORY**: Always describe user impact: Every changelog entry must explain how the change affects end users, not just what was changed technically. Focus on user value and practical implications.

**Required steps:**

- **Additional repositories (Step 6) - MANDATORY**: 
  - **⚠️ CRITICAL**: Always check the **website** repository for documentation changes during the release period. This is a required step and MUST NOT be skipped.
  - **⚠️ CRITICAL**: You MUST check ALL optional repositories (talm, boot-to-talos, cozypkg, cozy-proxy) for tags during the release period. Do NOT skip any repository even if you think there might not be tags.
  - **CRITICAL**: For ALL entries from additional repositories (website and optional), you MUST:
    - **MANDATORY**: Extract PR number from commit message first
    - **MANDATORY**: For commits with PR numbers, ALWAYS use `gh pr view <PR_NUMBER> --repo cozystack/<repo> --json author --jq .author.login` to get PR author (not commit author)
    - **MANDATORY**: Only for commits without PR numbers (rare), fallback to: `gh api repos/cozystack/<repo>/commits/<hash> --jq '.author.login'`
    - **MANDATORY**: Do NOT skip getting GitHub username via CLI - do this for EVERY commit
    - **MANDATORY**: Do NOT use commit author for PRs - always use PR author
    - Include PR link or commit hash reference
    - Format: `* **[repo] Description**: details ([**@username**](https://github.com/username) in cozystack/repo#123)`
  - For **optional repositories** (talm, boot-to-talos, cozypkg, cozy-proxy), you MUST check ALL of them for tags during the release period. Use the loop provided in Step 6 to check each repository systematically.
  - When including changes from additional repositories, use the format: `[repo-name] Description` and link to the repository's PR/issue if available
  - **Prefer PR numbers over commit hashes**: For commits from additional repositories, extract PR number from commit message using GitHub API. Use PR format (`cozystack/website#123`) instead of commit hash (`cozystack/website@abc1234`) when available
  - **Never add entries without author and PR/commit reference**: Every entry from additional repositories must have both author and link
  - Group changes from additional repositories with main repository changes, or create separate subsections if there are many changes from a specific repository

- **PR author verification (Step 7) - MANDATORY**:
  - **⚠️ CRITICAL**: You MUST get the author from PR using `gh pr view`, NOT from commit
  - **⚠️ CRITICAL**: Extract PR numbers from commit messages, then use `gh pr view <PR_NUMBER> --json author --jq .author.login` for each PR
  - **⚠️ CRITICAL**: Do NOT use commit author - commit author is usually the person who merged, not the person who wrote the code
  - **⚠️ CRITICAL**: Do NOT skip this step for any PR, even if the author seems obvious
  - For commits without PR numbers (rare), fall back to: `gh api repos/cozystack/cozystack/commits/<hash> --jq '.author.login'`
  - This ensures correct attribution and prevents errors in changelog entries (especially important for squash/merge commits)

- **Contributors list (Step 8)**: 
  - For minor releases (vX.Y.0): You must generate a list of all contributors and identify first-time contributors.
  - For patch releases: Contributors section is optional, but recommended for significant releases
  - Extract GitHub usernames from PR links in commit messages or changelog entries
  - This helps recognize community contributions and welcome new contributors
- **Minor releases (vX.Y.0)**:
  - Must include **all changes** from patch releases of the previous minor version (e.g., v0.38.0 includes all changes from v0.37.1, v0.37.2, v0.37.3, etc.)
  - The "Full Changelog" link must reference the previous minor release (v0.37.0...v0.38.0), NOT the last patch release (v0.37.8...v0.38.0)
  - This ensures users can see the complete set of changes for the entire minor version cycle
  - **Verification step**: After creating the changelog, extract all PR numbers from patch release changelogs and verify they all appear in the minor release changelog to prevent missing entries
  - **Backport handling**: Patch releases may contain backports with different PR numbers (e.g., #1624 in patch release vs #1622 in main). For minor releases, use original PR numbers from main when available, but verify that all changes from patch releases are included regardless of PR number differences
  - **Content verification**: Don't rely solely on PR number matching - verify that change descriptions from patch releases appear in the minor release changelog, as backports may have different PR numbers

