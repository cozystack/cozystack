#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLICKHOUSE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${CLICKHOUSE_DIR}/values.yaml"
VERSIONS_FILE="${CLICKHOUSE_DIR}/files/versions.yaml"
SERVER_REPO="clickhouse/clickhouse-server"
KEEPER_REPO="clickhouse/clickhouse-keeper"

# Check if jq is installed
if ! command -v jq &> /dev/null; then
    echo "Error: jq is not installed. Please install jq and try again." >&2
    exit 1
fi

# Supported major.minor versions (newest first). Keep this list to the
# ClickHouse LTS lines plus whatever is currently pinned as the default so
# existing installations are never forced onto a new major on regeneration.
SUPPORTED_MAJORS=("25.8" "25.3" "24.9")

# Fetch all X.Y.Z.W tags for a Docker Hub repository (paginated).
fetch_tags() {
    local repo="$1"
    local url="https://hub.docker.com/v2/repositories/${repo}/tags/?page_size=100"
    while [ -n "$url" ] && [ "$url" != "null" ]; do
        local page
        page="$(curl -sSL "$url")"
        echo "$page" | jq -r '.results[].name'
        url="$(echo "$page" | jq -r '.next')"
    done | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | sort -Vu
}

echo "Fetching tags from Docker Hub..."
SERVER_TAGS="$(fetch_tags "$SERVER_REPO")"
KEEPER_TAGS="$(fetch_tags "$KEEPER_REPO")"

# Only tags published for BOTH the server and the keeper image are usable —
# the chart pins a single version for both containers.
COMMON_TAGS="$(comm -12 <(echo "$SERVER_TAGS") <(echo "$KEEPER_TAGS"))"

if [ -z "$COMMON_TAGS" ]; then
    echo "Error: no tags common to ${SERVER_REPO} and ${KEEPER_REPO}" >&2
    exit 1
fi

# Build versions map: major.minor -> latest patch present in both images.
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_minor in "${SUPPORTED_MAJORS[@]}"; do
    MATCHING="$(echo "$COMMON_TAGS" | grep -E "^${major_minor//./\\.}\.[0-9]+\.[0-9]+$" | sort -V | tail -n1)"
    if [ -n "$MATCHING" ]; then
        VERSION_MAP["v${major_minor}"]="${MATCHING}"
        MAJOR_VERSIONS+=("v${major_minor}")
        echo "Found version: v${major_minor} -> ${MATCHING}"
    else
        echo "Warning: no tag found for ${major_minor} in both images, skipping..." >&2
    fi
done

if [ ${#MAJOR_VERSIONS[@]} -eq 0 ]; then
    echo "Error: no matching versions found" >&2
    exit 1
fi

# Write versions.yaml (newest first).
echo "Updating $VERSIONS_FILE..."
{
    for major_ver in "${MAJOR_VERSIONS[@]}"; do
        echo "\"${major_ver}\": \"${VERSION_MAP[$major_ver]}\""
    done
} > "$VERSIONS_FILE"

# Preserve the current default if it is still a supported major; otherwise
# fall back to the newest. This keeps a regeneration from silently bumping
# the major version of existing deployments.
CURRENT_DEFAULT="$(awk '/^version: / {print $2; exit}' "$VALUES_FILE" 2>/dev/null || true)"
DEFAULT_VERSION="${MAJOR_VERSIONS[0]}"
for major_ver in "${MAJOR_VERSIONS[@]}"; do
    if [ "$major_ver" = "$CURRENT_DEFAULT" ]; then
        DEFAULT_VERSION="$CURRENT_DEFAULT"
        break
    fi
done

# Build new @enum/@param block for values.yaml.
NEW_VERSION_SECTION="## @enum {string} Version"
for major_ver in "${MAJOR_VERSIONS[@]}"; do
    NEW_VERSION_SECTION="${NEW_VERSION_SECTION}
## @value $major_ver"
done
NEW_VERSION_SECTION="${NEW_VERSION_SECTION}

## @param {Version} version - ClickHouse major.minor version to deploy. Applies to both the ClickHouse server and ClickHouse Keeper images.
version: ${DEFAULT_VERSION}"

TEMP_FILE="$(mktemp)"
trap 'rm -f "$TEMP_FILE"' EXIT

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
    mv "$TEMP_FILE" "$VALUES_FILE"
else
    echo "Inserting new version section in $VALUES_FILE..."
    awk -v new_section="$NEW_VERSION_SECTION" '
        /^## @section Application-specific parameters/ && !done {
            print new_section
            print ""
            done = 1
        }
        { print }
    ' "$VALUES_FILE" > "$TEMP_FILE"
    mv "$TEMP_FILE" "$VALUES_FILE"
fi

echo "Successfully updated $VALUES_FILE with versions: ${MAJOR_VERSIONS[*]} (default ${DEFAULT_VERSION})"
