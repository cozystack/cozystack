#!/bin/bash
###############################################################################
# check-optional-repos.sh - Check optional repositories for tags and commits #
# during a release period                                                     #
###############################################################################
set -eu

# Function to ensure repository is cloned and up-to-date
update_repo() {
  local repo_name=$1
  local repo_url="https://github.com/cozystack/${repo_name}.git"
  
  mkdir -p _repos
  cd _repos
  
  if [ -d "$repo_name" ]; then
    cd "$repo_name"
    git fetch --all --tags --force
    git checkout main 2>/dev/null || git checkout master
    git pull
  else
    git clone "$repo_url"
    cd "$repo_name"
  fi
  
  cd ../..
}

# Check if required parameters are provided
if [ $# -lt 2 ]; then
  echo "Usage: $0 <RELEASE_START> <RELEASE_END>"
  echo "Example: $0 '2025-10-10 12:27:31 +0400' '2025-10-13 16:04:33 +0200'"
  exit 1
fi

RELEASE_START="$1"
RELEASE_END="$2"

# Get the script directory to return to it later
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
COZYSTACK_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

cd "$COZYSTACK_ROOT"

echo "Checking optional repositories for tags and commits between:"
echo "  Start: $RELEASE_START"
echo "  End: $RELEASE_END"
echo ""

# Loop through ALL optional repositories
for repo_name in talm boot-to-talos cozyhr cozy-proxy; do
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "Checking repository: $repo_name"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  
  # Update/clone repository
  update_repo "$repo_name"
  
  cd "_repos/$repo_name"
  REPO_NAME=$(basename "$(pwd)")
  git fetch --all --tags --force
  
  # Check for tags matching release version pattern or created during release period
  TAGS=$(git for-each-ref --format='%(refname:short) %(creatordate)' refs/tags 2>/dev/null | \
    awk -v start="$RELEASE_START" -v end="$RELEASE_END" '$2 >= start && $2 <= end {print $1}' || true)
  
  if [ -n "$TAGS" ]; then
    echo "Found tags in $repo_name: $TAGS"
    PREV_TAG=$(echo "$TAGS" | head -1)
    NEW_TAG=$(echo "$TAGS" | tail -1)
    
    echo ""
    echo "Commits between $PREV_TAG and $NEW_TAG:"
    # Include merge commits to capture backports
    git log "$PREV_TAG..$NEW_TAG" --format="%H|%s|%an" 2>/dev/null | while IFS='|' read -r commit_hash subject author_name; do
      if [ -z "$commit_hash" ]; then
        continue
      fi
      
      # Get PR number from commit message
      COMMIT_MSG=$(git log -1 --format=%B "$commit_hash" 2>/dev/null || echo "")
      PR_NUMBER=$(echo "$COMMIT_MSG" | grep -oE '#[0-9]+' | head -1 | tr -d '#' || echo "")
      
      # Get author: prioritize PR author, fallback to commit author
      GITHUB_USERNAME=""
      if [ -n "$PR_NUMBER" ]; then
        GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --repo "cozystack/$REPO_NAME" --json author --jq '.author.login // empty' 2>/dev/null || echo "")
      fi
      if [ -z "$GITHUB_USERNAME" ]; then
        GITHUB_USERNAME=$(gh api "repos/cozystack/$REPO_NAME/commits/$commit_hash" --jq '.author.login // empty' 2>/dev/null || echo "")
      fi
      
      if [ -n "$PR_NUMBER" ]; then
        echo "  $commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/$REPO_NAME#$PR_NUMBER"
      else
        echo "  $commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/$REPO_NAME@${commit_hash:0:7}"
      fi
    done
  else
    echo "No tags found in $repo_name during release period"
    
    # Check for commits by dates if no exact version tags
    # Include merge commits to capture backports
    COMMITS=$(git log --since="$RELEASE_START" --until="$RELEASE_END" --format="%H|%s|%an" 2>/dev/null || true)
    
    if [ -n "$COMMITS" ]; then
      echo ""
      echo "Commits found by date range:"
      echo "$COMMITS" | while IFS='|' read -r commit_hash subject author_name; do
        if [ -z "$commit_hash" ]; then
          continue
        fi
        
        # Get PR number from commit message
        COMMIT_MSG=$(git log -1 --format=%B "$commit_hash" 2>/dev/null || echo "")
        PR_NUMBER=$(echo "$COMMIT_MSG" | grep -oE '#[0-9]+' | head -1 | tr -d '#' || echo "")
        
        # Get author: prioritize PR author, fallback to commit author
        GITHUB_USERNAME=""
        if [ -n "$PR_NUMBER" ]; then
          GITHUB_USERNAME=$(gh pr view "$PR_NUMBER" --repo "cozystack/$REPO_NAME" --json author --jq '.author.login // empty' 2>/dev/null || echo "")
        fi
        if [ -z "$GITHUB_USERNAME" ]; then
          GITHUB_USERNAME=$(gh api "repos/cozystack/$REPO_NAME/commits/$commit_hash" --jq '.author.login // empty' 2>/dev/null || echo "")
        fi
        
        if [ -n "$PR_NUMBER" ]; then
          echo "  $commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/$REPO_NAME#$PR_NUMBER"
        else
          echo "  $commit_hash|$subject|$author_name|$GITHUB_USERNAME|cozystack/$REPO_NAME@${commit_hash:0:7}"
        fi
      done
    else
      echo "No commits found in $repo_name during release period"
    fi
  fi
  
  echo ""
  cd "$COZYSTACK_ROOT"
done

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "Finished checking all optional repositories"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

