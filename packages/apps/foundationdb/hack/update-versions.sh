#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FOUNDATIONDB_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${FOUNDATIONDB_DIR}/values.yaml"
VERSIONS_FILE="${FOUNDATIONDB_DIR}/files/versions.yaml"
OPERATOR_VALUES_FILE="${FOUNDATIONDB_DIR}/../../system/foundationdb-operator/charts/fdb-operator/values.yaml"

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

# Check if yq is installed
if ! command -v yq &> /dev/null; then
    echo "Error: yq is not installed. Please install yq and try again." >&2
    exit 1
fi

# The operator talks to a cluster through the client library of the cluster's
# major.minor, and it only carries the ones its chart lists as init containers,
# so those are the versions a cluster can run.
echo "Reading supported major.minor versions from $OPERATOR_VALUES_FILE..."
SUPPORTED_VERSIONS=$(yq -r '.initContainers | keys | .[]' "$OPERATOR_VALUES_FILE" | sort -V | xargs)

if [ -z "$SUPPORTED_VERSIONS" ]; then
    echo "Error: Could not read supported versions from the operator chart" >&2
    exit 1
fi

echo "Supported major.minor versions: $SUPPORTED_VERSIONS"

# imageType split runs foundationdb + foundationdb-kubernetes-sidecar (tagged
# <version>-1), unified runs fdb-kubernetes-monitor, so a version is usable
# only when all three are published.
echo "Fetching available image tags from registry..."
SERVER_TAGS=$(skopeo list-tags docker://docker.io/foundationdb/foundationdb | jq -r '.Tags[] | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))' | sort -V)
SIDECAR_TAGS=$(skopeo list-tags docker://docker.io/foundationdb/foundationdb-kubernetes-sidecar | jq -r '.Tags[]')
MONITOR_TAGS=$(skopeo list-tags docker://docker.io/foundationdb/fdb-kubernetes-monitor | jq -r '.Tags[]')

if [ -z "$SERVER_TAGS" ] || [ -z "$SIDECAR_TAGS" ] || [ -z "$MONITOR_TAGS" ]; then
    echo "Error: Could not fetch available image tags" >&2
    exit 1
fi

# Build versions map: major.minor version -> latest patch version
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_version in $SUPPORTED_VERSIONS; do
    latest_tag=""
    for tag in $(echo "$SERVER_TAGS" | grep "^${major_version//./\\.}\\." || true); do
        if echo "$SIDECAR_TAGS" | grep -qx "${tag}-1" && echo "$MONITOR_TAGS" | grep -qx "${tag}"; then
            latest_tag="$tag"
        fi
    done

    if [ -n "$latest_tag" ]; then
        VERSION_MAP["v${major_version}"]="${latest_tag}"
        MAJOR_VERSIONS+=("v${major_version}")
        echo "Found version: v${major_version} -> ${latest_tag}"
    else
        echo "Warning: No complete set of images found for ${major_version}, skipping..." >&2
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

## @param {Version} version - FoundationDB major.minor version to deploy
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
    # Version section doesn't exist, insert it before the cluster configuration
    echo "Inserting new version section in $VALUES_FILE..."

    awk -v new_section="$NEW_VERSION_SECTION" '
        /^## @typedef {struct} ClusterProcessCounts/ {
            print new_section
            print ""
        }
        { print }
    ' "$VALUES_FILE" > "$TEMP_FILE.tmp"
    mv "$TEMP_FILE.tmp" "$VALUES_FILE"
fi

echo "Successfully updated $VALUES_FILE with major.minor versions: ${MAJOR_VERSIONS[*]}"
