#!/bin/sh

if [ $# -ne 1 ]; then
  echo "Usage: $0 <version>"
  echo "Example: 0.37.*"
  exit 1
fi

VERSION_PATTERN="$1"

# Collect matching files first
FILES=$(find docs/changelogs -name "v${VERSION_PATTERN}.md" 2>/dev/null || true)

if [ -z "$FILES" ]; then
  echo "No changelog files found matching pattern: v${VERSION_PATTERN}.md"
  exit 1
fi

# Process each file
echo "$FILES" | while IFS= read -r file; do
  if [ -z "$file" ]; then
    continue
  fi
  
  # Extract version from filename safely (basename without extension)
  version=$(basename "$file" .md)
  
  if [ -z "$version" ]; then
    echo "Warning: Could not extract version from file: $file"
    continue
  fi
  
  echo "Uploading release notes for version: $version"
  
  # Check exit status of gh release edit
  if ! gh release edit "$version" --notes-file "docs/changelogs/${version}.md"; then
    echo "Error: Failed to upload release notes for version: $version"
    exit 1
  fi
done
