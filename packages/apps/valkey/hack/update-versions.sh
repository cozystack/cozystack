#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALKEY_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${VALKEY_DIR}/values.yaml"
VERSIONS_FILE="${VALKEY_DIR}/files/versions.yaml"
VALKEY_IMAGE="docker://docker.io/valkey/valkey"

# Major versions we keep offering, newest first. We deliberately track the 8.x
# line (default) and the 7.2.x line — the latter is the BSD-3-Clause fork of
# Redis 7.2.4 and the most conservative drop-in target. Bumping the offered
# majors (e.g. to add 9.x) is an explicit edit to this list.
MAJORS=(8 7)

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
AVAILABLE_TAGS=$(skopeo list-tags "${VALKEY_IMAGE}" | jq -r '.Tags[] | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))' | sort -V)

if [ -z "$AVAILABLE_TAGS" ]; then
    echo "Error: Could not fetch available image tags" >&2
    exit 1
fi

# Build versions map: major version -> latest patch version
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_version in "${MAJORS[@]}"; do
    matching_tags=$(echo "$AVAILABLE_TAGS" | grep "^${major_version}\\.")
    if [ -n "$matching_tags" ]; then
        latest_tag=$(echo "$matching_tags" | tail -n1)
        VERSION_MAP["v${major_version}"]="${latest_tag}"
        MAJOR_VERSIONS+=("v${major_version}")
        echo "Found version: v${major_version} -> ${latest_tag}"
    else
        echo "Warning: Could not find any patch versions for ${major_version}, skipping..." >&2
    fi
done

if [ ${#MAJOR_VERSIONS[@]} -eq 0 ]; then
    echo "Error: No matching versions found" >&2
    exit 1
fi

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

## @param {Version} version - Valkey major version to deploy
version: ${MAJOR_VERSIONS[0]}"

# Check if version section already exists
if grep -q "^## @enum {string} Version" "$VALUES_FILE"; then
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
    ' "$VALUES_FILE" > "$TEMP_FILE"
    cat "$TEMP_FILE" > "$VALUES_FILE"
else
    echo "Inserting new version section in $VALUES_FILE..."
    awk -v new_section="$NEW_VERSION_SECTION" '
        /^## @section Application-specific parameters/ {
            print new_section
            print ""
        }
        { print }
    ' "$VALUES_FILE" > "$TEMP_FILE"
    cat "$TEMP_FILE" > "$VALUES_FILE"
fi

echo "Successfully updated $VALUES_FILE with major versions: ${MAJOR_VERSIONS[*]}"
