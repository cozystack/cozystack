#!/usr/bin/env bash
#
# Regenerate the ClickHouse version map (files/versions.yaml) and the version
# enum in values.yaml from the Docker Hub tags of clickhouse/clickhouse-server
# and clickhouse/clickhouse-keeper. Run via `make update`.
#
# Portable to stock macOS (bash 3.2, BSD awk/sort): no associative arrays and
# no `awk -v` with embedded newlines. For tests, CH_SERVER_TAGS_FILE /
# CH_KEEPER_TAGS_FILE inject the tag lists offline and VALUES_FILE /
# VERSIONS_FILE redirect the outputs.

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLICKHOUSE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
VALUES_FILE="${VALUES_FILE:-${CLICKHOUSE_DIR}/values.yaml}"
VERSIONS_FILE="${VERSIONS_FILE:-${CLICKHOUSE_DIR}/files/versions.yaml}"
SERVER_REPO="clickhouse/clickhouse-server"
KEEPER_REPO="clickhouse/clickhouse-keeper"

# Major.minor lines to publish, newest first. Overridable for tests.
: "${CH_SUPPORTED_MAJORS:=25.8 25.3 24.9}"

# Patch pins: a major frozen at an exact patch regardless of newer tags, so a
# regeneration never changes the image of existing installs on that line. The
# default (v24.9) is pinned to the tag the chart shipped before the version
# parameter existed, preserving byte-for-byte identical rendering.
pin_for() {
  case "$1" in
    v24.9) printf '24.9.2.42' ;;
    *) printf '' ;;
  esac
}

# Numeric sort of X.Y.Z.W tags — portable across BSD and GNU sort (no -V).
version_sort() { sort -t. -k1,1n -k2,2n -k3,3n -k4,4n; }

# Emit every X.Y.Z.W tag of a Docker Hub repo, byte-collated and unique.
# Overridable offline via a file named by $2 (a variable name).
fetch_tags() {
  local repo="$1" file_var="$2" file
  file="${!file_var:-}"
  if [ -n "$file" ]; then
    grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' "$file" | LC_ALL=C sort -u
    return
  fi
  command -v jq >/dev/null 2>&1 || { echo "Error: jq is required to fetch tags" >&2; exit 1; }
  local url="https://hub.docker.com/v2/repositories/${repo}/tags/?page_size=100"
  while [ -n "$url" ] && [ "$url" != "null" ]; do
    local page
    page="$(curl -sSL "$url")"
    printf '%s\n' "$page" | jq -r '.results[].name'
    url="$(printf '%s\n' "$page" | jq -r '.next')"
  done | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | LC_ALL=C sort -u
}

echo "Resolving ClickHouse versions..." >&2
SERVER_TAGS="$(fetch_tags "$SERVER_REPO" CH_SERVER_TAGS_FILE)"
KEEPER_TAGS="$(fetch_tags "$KEEPER_REPO" CH_KEEPER_TAGS_FILE)"

# Tags present in BOTH images. comm walks its inputs in byte order, so they
# must be byte-collated (fetch_tags emits `LC_ALL=C sort -u`); a version
# collation would silently drop common tags at every X.9 -> X.10 boundary.
COMMON_TAGS="$(comm -12 <(printf '%s\n' "$SERVER_TAGS") <(printf '%s\n' "$KEEPER_TAGS"))"
[ -n "$COMMON_TAGS" ] || { echo "Error: no tag common to ${SERVER_REPO} and ${KEEPER_REPO}" >&2; exit 1; }

# Resolve each configured major to one tag: its pin if set (and available),
# else the latest common patch. A configured major with no common tag is a
# hard error raised BEFORE any file is written, so a partial Docker Hub result
# never silently drops a supported version or leaves a half-updated tree.
MAJORS=()
TAGS=()
for mm in $CH_SUPPORTED_MAJORS; do
  key="v${mm}"
  mm_re="${mm//./\\.}"
  latest="$(printf '%s\n' "$COMMON_TAGS" | grep -E "^${mm_re}\.[0-9]+\.[0-9]+$" | version_sort | tail -n1 || true)"
  pin="$(pin_for "$key")"
  if [ -n "$pin" ]; then
    if printf '%s\n' "$COMMON_TAGS" | grep -qx "$pin"; then
      tag="$pin"
    else
      echo "Error: pinned tag ${pin} for ${key} is not available in both images" >&2
      exit 1
    fi
  elif [ -n "$latest" ]; then
    tag="$latest"
  else
    echo "Error: no tag found for configured version ${mm} in both images" >&2
    exit 1
  fi
  MAJORS+=("$key")
  TAGS+=("$tag")
  echo "Resolved ${key} -> ${tag}" >&2
done

# Build both artifacts in temp files and move them into place only once both
# are ready, so a failure mid-run leaves the committed files untouched.
TMP_VERSIONS="$(mktemp)"
TMP_BLOCK="$(mktemp)"
TMP_VALUES="$(mktemp)"
trap 'rm -f "$TMP_VERSIONS" "$TMP_BLOCK" "$TMP_VALUES"' EXIT

i=0
while [ "$i" -lt "${#MAJORS[@]}" ]; do
  printf '"%s": "%s"\n' "${MAJORS[$i]}" "${TAGS[$i]}" >> "$TMP_VERSIONS"
  i=$((i + 1))
done

# Keep the current default if it is still supported, else fall back to newest.
CURRENT_DEFAULT="$(awk '/^version:[[:space:]]/ {print $2; exit}' "$VALUES_FILE" 2>/dev/null || true)"
DEFAULT_VERSION="${MAJORS[0]}"
i=0
while [ "$i" -lt "${#MAJORS[@]}" ]; do
  [ "${MAJORS[$i]}" = "$CURRENT_DEFAULT" ] && DEFAULT_VERSION="$CURRENT_DEFAULT"
  i=$((i + 1))
done

# The values.yaml @enum/@param block, built in a file (avoids `awk -v` with
# embedded newlines, which BSD awk rejects).
{
  echo "## @enum {string} Version"
  i=0
  while [ "$i" -lt "${#MAJORS[@]}" ]; do
    echo "## @value ${MAJORS[$i]}"
    i=$((i + 1))
  done
  echo ""
  echo "## @param {Version} version - ClickHouse major.minor version to deploy. Applies to both the ClickHouse server and ClickHouse Keeper images. Downgrading to an older major is unsafe (ClickHouse cannot read data written by a newer server and Keeper snapshots are not backward compatible) — only increase this value."
  echo "version: ${DEFAULT_VERSION}"
} > "$TMP_BLOCK"

# Splice the block into values.yaml with pure bash: replace an existing
# version section, or insert before the Application-specific section.
replaced=0
in_section=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in
    "## @enum {string} Version")
      cat "$TMP_BLOCK" >> "$TMP_VALUES"; in_section=1; replaced=1; continue ;;
  esac
  if [ "$in_section" -eq 1 ]; then
    case "$line" in
      version:*) in_section=0; continue ;;
      *) continue ;;
    esac
  fi
  if [ "$replaced" -eq 0 ] && [ "$line" = "## @section Application-specific parameters" ]; then
    cat "$TMP_BLOCK" >> "$TMP_VALUES"; echo "" >> "$TMP_VALUES"; replaced=1
  fi
  printf '%s\n' "$line" >> "$TMP_VALUES"
done < "$VALUES_FILE"

[ "$replaced" -eq 1 ] || { echo "Error: could not place the version block in $VALUES_FILE" >&2; exit 1; }

mv "$TMP_VERSIONS" "$VERSIONS_FILE"
mv "$TMP_VALUES" "$VALUES_FILE"
echo "Updated $VERSIONS_FILE and $VALUES_FILE (default ${DEFAULT_VERSION})" >&2
