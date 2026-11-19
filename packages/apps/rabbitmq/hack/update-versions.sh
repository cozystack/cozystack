#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RABBITMQ_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${RABBITMQ_DIR}/values.yaml"
VERSIONS_FILE="${RABBITMQ_DIR}/files/versions.yaml"
GITHUB_API_URL="https://api.github.com/repos/rabbitmq/rabbitmq-server/releases"

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed. Please install jq and try again." >&2
    exit 1
fi

# Fetch releases from GitHub API
echo "Fetching releases from GitHub API..."
RELEASES_JSON=$(curl -sSL "${GITHUB_API_URL}?per_page=100")

if [ -z "$RELEASES_JSON" ]; then
    echo "Error: Could not fetch releases from GitHub API" >&2
    exit 1
fi

# Extract stable release tags (format: v3.13.7, v4.0.3, etc.)
# Filter out pre-releases and draft releases
RELEASE_TAGS=$(echo "$RELEASES_JSON" | jq -r '.[] | select(.prerelease == false) | select(.draft == false) | .tag_name' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V)

if [ -z "$RELEASE_TAGS" ]; then
    echo "Error: Could not find any stable release tags" >&2
    exit 1
fi

echo "Found release tags: $(echo "$RELEASE_TAGS" | tr '\n' ' ')"

# Supported major.minor versions (newest first)
# We support the last few minor releases of each active major
SUPPORTED_MAJORS=("4.2" "4.1" "4.0" "3.13")

# Build versions map: major.minor -> latest patch version
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_minor in "${SUPPORTED_MAJORS[@]}"; do
    # Find the latest patch version for this major.minor
    MATCHING=$(echo "$RELEASE_TAGS" | grep -E "^v${major_minor//./\\.}\.[0-9]+$" | tail -n1)

    if [ -n "$MATCHING" ]; then
        # Strip the 'v' prefix for the value (Docker tag format is e.g. 3.13.7)
        TAG_VERSION="${MATCHING#v}"
        VERSION_MAP["v${major_minor}"]="${TAG_VERSION}"
        MAJOR_VERSIONS+=("v${major_minor}")
        echo "Found version: v${major_minor} -> ${TAG_VERSION}"
    else
        echo "Warning: No stable releases found for ${major_minor}, skipping..." >&2
    fi
done

if [ ${#MAJOR_VERSIONS[@]} -eq 0 ]; then
    echo "Error: No matching versions found" >&2
    exit 1
fi

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

## @param {Version} version - RabbitMQ major.minor version to deploy
version: ${MAJOR_VERSIONS[0]}"

# Check if version section already exists
if grep -q "^## @enum {string} Version" "$VALUES_FILE"; then
    # Version section exists, update it using awk
    echo "Updating existing version section in $VALUES_FILE..."

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
