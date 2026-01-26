#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MYSQL_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${MYSQL_DIR}/values.yaml"
VERSIONS_FILE="${MYSQL_DIR}/files/versions.yaml"
MARIADB_API_URL="https://downloads.mariadb.org/rest-api/mariadb/"

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed. Please install jq and try again." >&2
    exit 1
fi

# Get LTS versions from MariaDB REST API
echo "Fetching LTS versions from MariaDB REST API..."
LTS_VERSIONS_JSON=$(curl -sSL "${MARIADB_API_URL}")

if [ -z "$LTS_VERSIONS_JSON" ]; then
    echo "Error: Could not fetch versions from MariaDB REST API" >&2
    exit 1
fi

# Extract LTS stable major versions
LTS_MAJOR_VERSIONS=$(echo "$LTS_VERSIONS_JSON" | jq -r '.major_releases[] | select(.release_support_type == "Long Term Support") | select(.release_status == "Stable") | .release_id' | sort -V -r)

if [ -z "$LTS_MAJOR_VERSIONS" ]; then
    echo "Error: Could not find any LTS stable versions" >&2
    exit 1
fi

echo "Found LTS major versions: $(echo "$LTS_MAJOR_VERSIONS" | tr '\n' ' ')"

# Build versions map: major version -> latest patch version
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_version in $LTS_MAJOR_VERSIONS; do
    echo "Fetching patch versions for ${major_version}..."
    
    # Get patch versions for this major version
    PATCH_VERSIONS_JSON=$(curl -sSL "${MARIADB_API_URL}${major_version}")
    
    if [ -z "$PATCH_VERSIONS_JSON" ]; then
        echo "Warning: Could not fetch patch versions for ${major_version}, skipping..." >&2
        continue
    fi
    
    # Extract all stable patch version IDs (format: MAJOR.MINOR.PATCH)
    # Filter only Stable releases
    PATCH_VERSIONS=$(echo "$PATCH_VERSIONS_JSON" | jq -r --arg major "$major_version" '.releases | to_entries[] | select(.key | startswith($major + ".")) | select(.value.release_status == "Stable") | .key' | sort -V)
    
    # If no stable releases found, try to get any releases (for backwards compatibility)
    if [ -z "$PATCH_VERSIONS" ]; then
        PATCH_VERSIONS=$(echo "$PATCH_VERSIONS_JSON" | jq -r '.releases | keys[]' | grep -E "^${major_version}\." | sort -V)
    fi
    
    if [ -z "$PATCH_VERSIONS" ]; then
        echo "Warning: Could not find any patch versions for ${major_version}, skipping..." >&2
        continue
    fi
    
    # Get the latest patch version
    LATEST_PATCH=$(echo "$PATCH_VERSIONS" | tail -n1)
    
    # major_version already has format MAJOR.MINOR (e.g., "11.8")
    VERSION_MAP["v${major_version}"]="${LATEST_PATCH}"
    MAJOR_VERSIONS+=("v${major_version}")
    echo "Found version: v${major_version} -> ${LATEST_PATCH}"
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

# Update values.yaml - enum with major.minor versions only
TEMP_FILE=$(mktemp)
trap "rm -f $TEMP_FILE" EXIT

# Build new version section
NEW_VERSION_SECTION="## @enum {string} Version"
for major_ver in "${MAJOR_VERSIONS[@]}"; do
    NEW_VERSION_SECTION="${NEW_VERSION_SECTION}
## @value $major_ver"
done
NEW_VERSION_SECTION="${NEW_VERSION_SECTION}

## @param {Version} version - MariaDB major.minor version to deploy
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

echo "Successfully updated $VALUES_FILE with major.minor versions: ${MAJOR_VERSIONS[*]}"

