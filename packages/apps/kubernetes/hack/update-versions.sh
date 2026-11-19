#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KUBERNETES_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${KUBERNETES_DIR}/values.yaml"
VERSIONS_FILE="${KUBERNETES_DIR}/files/versions.yaml"
MAKEFILE="${KUBERNETES_DIR}/Makefile"
KAMAJI_DOCKERFILE="${KUBERNETES_DIR}/../../system/kamaji/images/kamaji/Dockerfile"

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

# Get kamaji version from Dockerfile
echo "Reading kamaji version from Dockerfile..."
if [ ! -f "$KAMAJI_DOCKERFILE" ]; then
    echo "Error: Kamaji Dockerfile not found at $KAMAJI_DOCKERFILE" >&2
    exit 1
fi

KAMAJI_VERSION=$(grep "^ARG VERSION=" "$KAMAJI_DOCKERFILE" | cut -d= -f2 | tr -d '"')
if [ -z "$KAMAJI_VERSION" ]; then
    echo "Error: Could not extract kamaji version from Dockerfile" >&2
    exit 1
fi

echo "Kamaji version: $KAMAJI_VERSION"

# Get Kubernetes version from kamaji repository
echo "Fetching Kubernetes version from kamaji repository..."
KUBERNETES_VERSION_FROM_KAMAJI=$(curl -sSL "https://raw.githubusercontent.com/clastix/kamaji/${KAMAJI_VERSION}/internal/upgrade/kubeadm_version.go" | grep "KubeadmVersion" | sed -E 's/.*KubeadmVersion = "([^"]+)".*/\1/')

if [ -z "$KUBERNETES_VERSION_FROM_KAMAJI" ]; then
    echo "Error: Could not fetch Kubernetes version from kamaji repository" >&2
    exit 1
fi

echo "Kubernetes version from kamaji: $KUBERNETES_VERSION_FROM_KAMAJI"

# Extract major.minor version (e.g., "1.33" from "v1.33.0")
KUBERNETES_MAJOR_MINOR=$(echo "$KUBERNETES_VERSION_FROM_KAMAJI" | sed -E 's/v([0-9]+)\.([0-9]+)\.[0-9]+/\1.\2/')
KUBERNETES_MAJOR=$(echo "$KUBERNETES_MAJOR_MINOR" | cut -d. -f1)
KUBERNETES_MINOR=$(echo "$KUBERNETES_MAJOR_MINOR" | cut -d. -f2)

echo "Kubernetes major.minor: $KUBERNETES_MAJOR_MINOR"

# Get available image tags
echo "Fetching available image tags from registry..."
AVAILABLE_TAGS=$(skopeo list-tags docker://registry.k8s.io/kube-apiserver | jq -r '.Tags[] | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+$"))' | sort -V)

if [ -z "$AVAILABLE_TAGS" ]; then
    echo "Error: Could not fetch available image tags" >&2
    exit 1
fi

# Filter out versions higher than KUBERNETES_VERSION_FROM_KAMAJI
echo "Filtering versions above ${KUBERNETES_VERSION_FROM_KAMAJI}..."
FILTERED_TAGS=$(echo "$AVAILABLE_TAGS" | while read tag; do
    if [ -n "$tag" ]; then
        # Compare tag with KUBERNETES_VERSION_FROM_KAMAJI using version sort
        # Include tag if it's less than or equal to KUBERNETES_VERSION_FROM_KAMAJI
        if [ "$(printf '%s\n%s\n' "$tag" "$KUBERNETES_VERSION_FROM_KAMAJI" | sort -V | head -1)" = "$tag" ] || [ "$tag" = "$KUBERNETES_VERSION_FROM_KAMAJI" ]; then
            echo "$tag"
        fi
    fi
done)

if [ -z "$FILTERED_TAGS" ]; then
    echo "Error: No versions found after filtering" >&2
    exit 1
fi

AVAILABLE_TAGS="$FILTERED_TAGS"
echo "Filtered to $(echo "$AVAILABLE_TAGS" | wc -l | tr -d ' ') versions"

# Find the latest patch version for the supported major.minor version
echo "Finding latest patch version for ${KUBERNETES_MAJOR_MINOR}..."
SUPPORTED_PATCH_TAGS=$(echo "$AVAILABLE_TAGS" | grep "^v${KUBERNETES_MAJOR}\\.${KUBERNETES_MINOR}\\.")
if [ -z "$SUPPORTED_PATCH_TAGS" ]; then
    echo "Error: Could not find any patch versions for ${KUBERNETES_MAJOR_MINOR}" >&2
    exit 1
fi
KUBERNETES_VERSION=$(echo "$SUPPORTED_PATCH_TAGS" | tail -n1)
echo "Using latest patch version: $KUBERNETES_VERSION"

# Build versions map: major.minor -> latest patch version
# First, collect all unique major.minor versions from available tags
echo "Collecting all available major.minor versions..."
ALL_MAJOR_MINOR_VERSIONS=$(echo "$AVAILABLE_TAGS" | sed -E 's/v([0-9]+)\.([0-9]+)\.[0-9]+/v\1.\2/' | sort -V -u)

# Find the position of the supported version in the sorted list
SUPPORTED_MAJOR_MINOR="v${KUBERNETES_MAJOR}.${KUBERNETES_MINOR}"
echo "Looking for supported version: $SUPPORTED_MAJOR_MINOR"

# Get all versions that are <= supported version
# Create a temporary file for filtering
TEMP_VERSIONS=$(mktemp)

echo "$ALL_MAJOR_MINOR_VERSIONS" | while read version; do
    # Compare versions using sort -V (version sort)
    # If version <= supported, include it
    if [ "$(printf '%s\n%s\n' "$version" "$SUPPORTED_MAJOR_MINOR" | sort -V | head -1)" = "$version" ] || [ "$version" = "$SUPPORTED_MAJOR_MINOR" ]; then
        echo "$version"
    fi
done > "$TEMP_VERSIONS"

# Get the supported version and 5 previous versions (total 6 versions)
# First, find the position of supported version
SUPPORTED_POS=$(grep -n "^${SUPPORTED_MAJOR_MINOR}$" "$TEMP_VERSIONS" | cut -d: -f1)

if [ -z "$SUPPORTED_POS" ]; then
    echo "Error: Supported version $SUPPORTED_MAJOR_MINOR not found in available versions" >&2
    exit 1
fi

# Calculate start position (5 versions before supported, or from beginning if less than 5 available)
TOTAL_LINES=$(wc -l < "$TEMP_VERSIONS" | tr -d ' ')
START_POS=$((SUPPORTED_POS - 5))
if [ $START_POS -lt 1 ]; then
    START_POS=1
fi

# Extract versions from START_POS to SUPPORTED_POS (inclusive)
CANDIDATE_VERSIONS=$(sed -n "${START_POS},${SUPPORTED_POS}p" "$TEMP_VERSIONS")

if [ -z "$CANDIDATE_VERSIONS" ]; then
    echo "Error: Could not find supported version $SUPPORTED_MAJOR_MINOR in available versions" >&2
    exit 1
fi

declare -A VERSION_MAP
VERSIONS=()

# Process each candidate version
for major_minor_key in $CANDIDATE_VERSIONS; do
    # Extract major and minor for matching
    major=$(echo "$major_minor_key" | sed -E 's/v([0-9]+)\.([0-9]+)/\1/')
    minor=$(echo "$major_minor_key" | sed -E 's/v([0-9]+)\.([0-9]+)/\2/')
    
    # Find all tags that match this major.minor version
    matching_tags=$(echo "$AVAILABLE_TAGS" | grep "^v${major}\\.${minor}\\.")
    
    if [ -n "$matching_tags" ]; then
        # Get the latest patch version for this major.minor version
        latest_tag=$(echo "$matching_tags" | tail -n1)
        
        VERSION_MAP["${major_minor_key}"]="${latest_tag}"
        VERSIONS+=("${major_minor_key}")
        echo "Found version: ${major_minor_key} -> ${latest_tag}"
    fi
done

if [ ${#VERSIONS[@]} -eq 0 ]; then
    echo "Error: No matching versions found" >&2
    exit 1
fi

# Sort versions in descending order (newest first)
IFS=$'\n' VERSIONS=($(printf '%s\n' "${VERSIONS[@]}" | sort -V -r))
unset IFS

echo "Versions to add: ${VERSIONS[*]}"

# Create/update versions.yaml file
echo "Updating $VERSIONS_FILE..."
{
    for ver in "${VERSIONS[@]}"; do
        echo "\"${ver}\": \"${VERSION_MAP[$ver]}\""
    done
} > "$VERSIONS_FILE"

echo "Successfully updated $VERSIONS_FILE"

# Update values.yaml - enum with major.minor versions only
TEMP_FILE=$(mktemp)
trap "rm -f $TEMP_FILE $TEMP_VERSIONS" EXIT

# Build new version section
NEW_VERSION_SECTION="## @enum {string} Version"
for ver in "${VERSIONS[@]}"; do
    NEW_VERSION_SECTION="${NEW_VERSION_SECTION}
## @value $ver"
done
NEW_VERSION_SECTION="${NEW_VERSION_SECTION}

## @param {Version} version - Kubernetes major.minor version to deploy
version: \"${VERSIONS[0]}\""

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

echo "Successfully updated $VALUES_FILE with versions: ${VERSIONS[*]}"

# Update KUBERNETES_VERSION in Makefile
# Extract major.minor from KUBERNETES_VERSION (e.g., "v1.33" from "v1.33.4")
KUBERNETES_MAJOR_MINOR_FOR_MAKEFILE=$(echo "$KUBERNETES_VERSION" | sed -E 's/v([0-9]+)\.([0-9]+)\.[0-9]+/v\1.\2/')

if grep -q "^KUBERNETES_VERSION" "$MAKEFILE"; then
    # Update existing KUBERNETES_VERSION line using awk
    echo "Updating KUBERNETES_VERSION in $MAKEFILE..."
    awk -v new_version="${KUBERNETES_MAJOR_MINOR_FOR_MAKEFILE}" '
        /^KUBERNETES_VERSION = / {
            print "KUBERNETES_VERSION = " new_version
            next
        }
        { print }
    ' "$MAKEFILE" > "$TEMP_FILE.tmp"
    mv "$TEMP_FILE.tmp" "$MAKEFILE"
    echo "Successfully updated KUBERNETES_VERSION in $MAKEFILE to ${KUBERNETES_MAJOR_MINOR_FOR_MAKEFILE}"
else
    echo "Warning: KUBERNETES_VERSION not found in $MAKEFILE" >&2
fi

