#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POSTGRES_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${POSTGRES_DIR}/values.yaml"
VERSIONS_FILE="${POSTGRES_DIR}/files/versions.yaml"

# Get supported major versions from GitHub README
echo "Fetching supported major versions from GitHub..."
SUPPORTED_MAJOR_VERSIONS=$(curl -sSL 'https://raw.githubusercontent.com/cloudnative-pg/postgres-containers/refs/heads/main/README.md' | sed -n '/# CNPG PostgreSQL Container Images/,/#/p' | awk -F' +| +' '$4 ~ /[0-9]+\-[0-9]+\-[0-9]+/ && $6 ~ /[0-9]+\-[0-9]+\-[0-9]+/ {print $2}' | sort -u | xargs)

if [ -z "$SUPPORTED_MAJOR_VERSIONS" ]; then
    echo "Error: Could not fetch supported major versions" >&2
    exit 1
fi

echo "Supported major versions: $SUPPORTED_MAJOR_VERSIONS"

# Check if skopeo is installed
if ! command -v skopeo &> /dev/null; then
    echo "Error: skopeo is not installed. Please install skopeo and try again." >&2
    exit 1
fi

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed. Please install jq and try again." >&2
    exit 1
fi

# Get available image tags
echo "Fetching available image tags from registry..."
AVAILABLE_TAGS=$(skopeo list-tags docker://ghcr.io/cloudnative-pg/postgresql | jq -r '.Tags[] | select(test("^[0-9]+\\.[0-9]+$"))' | sort -V)

if [ -z "$AVAILABLE_TAGS" ]; then
    echo "Error: Could not fetch available image tags" >&2
    exit 1
fi

# Build versions map: major version -> latest minor version
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_version in $SUPPORTED_MAJOR_VERSIONS; do
    # Extract major version number (e.g., "18" from "18.1")
    major_num=$(echo "$major_version" | cut -d. -f1)
    
    # Find all tags that match this major version
    matching_tags=$(echo "$AVAILABLE_TAGS" | grep "^${major_num}\\.")
    
    if [ -n "$matching_tags" ]; then
        # Get the latest minor version for this major version
        latest_tag=$(echo "$matching_tags" | tail -n1)
        VERSION_MAP["v${major_num}"]="v${latest_tag}"
        MAJOR_VERSIONS+=("v${major_num}")
        echo "Found version: v${major_num} -> v${latest_tag}"
    fi
done

if [ ${#MAJOR_VERSIONS[@]} -eq 0 ]; then
    echo "Error: No matching versions found" >&2
    exit 1
fi

# Sort major versions in descending order (newest first)
IFS=$'\n' MAJOR_VERSIONS=($(printf '%s\n' "${MAJOR_VERSIONS[@]}" | sort -V -r))
unset IFS

echo "Major versions to add: ${MAJOR_VERSIONS[*]}"

# Create/update versions.yaml file
echo "Updating $VERSIONS_FILE..."
{
    for major_ver in "${MAJOR_VERSIONS[@]}"; do
        echo "\"${major_ver}\": \"${VERSION_MAP[$major_ver]}\""
    done
} > "$VERSIONS_FILE"

echo "Successfully updated $VERSIONS_FILE"

# Update values.yaml - enum with major versions only
TEMP_FILE=$(mktemp)
trap "rm -f $TEMP_FILE" EXIT

# Build new version section
NEW_VERSION_SECTION="## @enum {string} Version"
for major_ver in "${MAJOR_VERSIONS[@]}"; do
    NEW_VERSION_SECTION="${NEW_VERSION_SECTION}
## @value $major_ver"
done
NEW_VERSION_SECTION="${NEW_VERSION_SECTION}

## @param {Version} version - PostgreSQL major version to deploy
version: ${MAJOR_VERSIONS[0]}"

# Check if version section already exists
if grep -q "^## @enum {string} Version" "$VALUES_FILE"; then
    # Version section exists, update it using awk
    echo "Updating existing version section in $VALUES_FILE..."
    
    # Use awk to replace the section from "## @enum {string} Version" to "version: " (inclusive)
    # Delete the old section and insert the new one
    awk -v new_section="$NEW_VERSION_SECTION" '
        /^## @enum {string} Version/ {
            in_section = 1
            print new_section
            next
        }
        in_section && /^version: / {
            in_section = 0
            next
        }
        in_section {
            next
        }
        { print }
    ' "$VALUES_FILE" > "$TEMP_FILE.tmp"
    mv "$TEMP_FILE.tmp" "$VALUES_FILE"
else
    # Version section doesn't exist, insert it before Application-specific parameters section
    echo "Inserting new version section in $VALUES_FILE..."
    
    # Use awk to insert before "## @section Application-specific parameters"
    awk -v new_section="$NEW_VERSION_SECTION" '
        /^## @section Application-specific parameters/ {
            print new_section
            print ""
        }
        { print }
    ' "$VALUES_FILE" > "$TEMP_FILE.tmp"
    mv "$TEMP_FILE.tmp" "$VALUES_FILE"
fi

echo "Successfully updated $VALUES_FILE with major versions: ${MAJOR_VERSIONS[*]}"
