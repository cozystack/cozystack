#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPENSEARCH_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${OPENSEARCH_DIR}/values.yaml"
VERSIONS_FILE="${OPENSEARCH_DIR}/files/versions.yaml"
OPERATOR_DOCKERFILE="${OPENSEARCH_DIR}/../../system/opensearch-operator/images/opensearch-operator/Dockerfile"

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

OPERATOR_VERSION=$(grep -F 'ARG VERSION=' "$OPERATOR_DOCKERFILE" | cut -d= -f2)

if [ -z "$OPERATOR_VERSION" ]; then
    echo "Error: Could not read the operator version from $OPERATOR_DOCKERFILE" >&2
    exit 1
fi

# The operator README carries the range of OpenSearch versions each operator
# release was tested with, e.g. "| 2.8.0 | 2.19.2 | latest 3.x | ... |".
echo "Fetching supported OpenSearch versions for operator ${OPERATOR_VERSION} from GitHub..."
COMPATIBILITY_ROW=$(curl -sSL "https://raw.githubusercontent.com/opensearch-project/opensearch-k8s-operator/${OPERATOR_VERSION}/README.md" \
    | sed -n '/^## Compatibility/,/^## /p' \
    | awk -F'|' -v op="${OPERATOR_VERSION#v}" '
        {
            n = split($2, ops, /<br>/)
            for (i = 1; i <= n; i++) {
                gsub(/^[ \t]+|[ \t]+$/, "", ops[i])
                if (ops[i] == op) {
                    gsub(/^[ \t]+|[ \t]+$/, "", $3)
                    gsub(/^[ \t]+|[ \t]+$/, "", $4)
                    print $3 "|" $4
                    exit
                }
            }
        }')

if [ -z "$COMPATIBILITY_ROW" ]; then
    echo "Error: Could not find operator ${OPERATOR_VERSION} in the compatibility table" >&2
    exit 1
fi

MIN_VERSION=${COMPATIBILITY_ROW%%|*}
MAX_VERSION=${COMPATIBILITY_ROW#*|}
MIN_MAJOR=$(echo "$MIN_VERSION" | grep -oE '[0-9]+' | head -n1)
MAX_MAJOR=$(echo "$MAX_VERSION" | grep -oE '[0-9]+' | head -n1)

if [ -z "$MIN_MAJOR" ] || [ -z "$MAX_MAJOR" ]; then
    echo "Error: Could not parse the compatibility range '${MIN_VERSION}' - '${MAX_VERSION}'" >&2
    exit 1
fi

echo "Supported OpenSearch versions: ${MIN_VERSION} - ${MAX_VERSION}"

# Dashboards is deployed with the same version as the cluster, so a version is
# usable only when both images are published.
echo "Fetching available image tags from registry..."
OPENSEARCH_TAGS=$(skopeo list-tags docker://docker.io/opensearchproject/opensearch | jq -r '.Tags[] | select(test("^[0-9]+\\.[0-9]+\\.[0-9]+$"))' | sort -V)
DASHBOARDS_TAGS=$(skopeo list-tags docker://docker.io/opensearchproject/opensearch-dashboards | jq -r '.Tags[]')

if [ -z "$OPENSEARCH_TAGS" ] || [ -z "$DASHBOARDS_TAGS" ]; then
    echo "Error: Could not fetch available image tags" >&2
    exit 1
fi

# version_le A B: true when A sorts at or before B
version_le() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n1)" = "$1" ]
}

# Build versions map: major version -> latest version within the tested range
declare -A VERSION_MAP
MAJOR_VERSIONS=()

for major_num in $(seq "$MIN_MAJOR" "$MAX_MAJOR"); do
    latest_tag=""
    for tag in $(echo "$OPENSEARCH_TAGS" | grep "^${major_num}\\." || true); do
        if [[ "$MIN_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && ! version_le "$MIN_VERSION" "$tag"; then
            continue
        fi
        if [[ "$MAX_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] && ! version_le "$tag" "$MAX_VERSION"; then
            continue
        fi
        if echo "$DASHBOARDS_TAGS" | grep -qx "$tag"; then
            latest_tag="$tag"
        fi
    done

    if [ -n "$latest_tag" ]; then
        VERSION_MAP["v${major_num}"]="${latest_tag}"
        MAJOR_VERSIONS+=("v${major_num}")
        echo "Found version: v${major_num} -> ${latest_tag}"
    else
        echo "Warning: No published version found for major ${major_num}, skipping..." >&2
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

## @param {Version} version - OpenSearch major version to deploy.
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
    # Version section doesn't exist, insert it after the topology spread policy
    echo "Inserting new version section in $VALUES_FILE..."

    awk -v new_section="$NEW_VERSION_SECTION" '
        { print }
        /^topologySpreadPolicy: / {
            print ""
            print new_section
        }
    ' "$VALUES_FILE" > "$TEMP_FILE.tmp"
    mv "$TEMP_FILE.tmp" "$VALUES_FILE"
fi

echo "Successfully updated $VALUES_FILE with major versions: ${MAJOR_VERSIONS[*]}"
