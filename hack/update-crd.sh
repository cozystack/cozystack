#!/usr/bin/env bash
set -euo pipefail

# Requirements: yq (v4), jq, base64
need() { command -v "$1" >/dev/null 2>&1 || { echo "need $1"; exit 1; }; }
need yq; need jq; need base64

CHART_YAML="${CHART_YAML:-Chart.yaml}"
VALUES_YAML="${VALUES_YAML:-values.yaml}"
SCHEMA_JSON="${SCHEMA_JSON:-values.schema.json}"
CRD_DIR="../../core/platform/bundles/*/applicationdefinitions"

[[ -f "$CHART_YAML" ]] || { echo "No $CHART_YAML found"; exit 1; }
[[ -f "$SCHEMA_JSON" ]] || { echo "No $SCHEMA_JSON found"; exit 1; }

# Read basics from Chart.yaml
NAME="$(yq -r '.name // ""' "$CHART_YAML")"
DESC="$(yq -r '.description // ""' "$CHART_YAML")"
ICON_PATH_RAW="$(yq -r '.icon // ""' "$CHART_YAML")"

if [[ -z "$NAME" ]]; then
  echo "Chart.yaml: .name is empty"; exit 1
fi

# Resolve icon path
# Accepts:
#   /logos/foo.svg  -> ./logos/foo.svg
#   logos/foo.svg   -> logos/foo.svg
#   ./logos/foo.svg -> ./logos/foo.svg
# Fallback: ./logos/${NAME}.svg
resolve_icon_path() {
  local p="$1"
  if [[ -z "$p" || "$p" == "null" ]]; then
    echo "./logos/${NAME}.svg"; return
  fi
  if [[ "$p" == /* ]]; then
    echo ".${p}"
  else
    echo "$p"
  fi
}
ICON_PATH="$(resolve_icon_path "$ICON_PATH_RAW")"

if [[ ! -f "$ICON_PATH" ]]; then
  # try fallback
  ALT="./logos/${NAME}.svg"
  if [[ -f "$ALT" ]]; then
    ICON_PATH="$ALT"
  else
    echo "Icon not found: $ICON_PATH"; exit 1
  fi
fi

# Base64 (portable: no -w / -b options)
ICON_B64="$(base64 < "$ICON_PATH" | tr -d '\n' | tr -d '\r')"

# Find path to output CRD YAML
OUT="$(find $CRD_DIR -type f -name "${NAME}.yaml" | head -n 1)"
if [[ -z "$OUT" ]]; then
  echo "Error: ApplicationDefinition file for '${NAME}' not found in ${CRD_DIR}"
  echo "Please create the file first in one of the following directories:"
  
  # Auto-detect existing directories
  BASE_DIR="../../core/platform/bundles"
  if [[ -d "$BASE_DIR" ]]; then
    for bundle_dir in "$BASE_DIR"/*/applicationdefinitions; do
      if [[ -d "$bundle_dir" ]]; then
        bundle_name="$(basename "$(dirname "$bundle_dir")")"
        echo "  touch ${bundle_dir}/${NAME}.yaml  # ${bundle_name}"
      fi
    done
  else
    # Fallback if base directory doesn't exist
    echo "  touch ../../core/platform/bundles/iaas/applicationdefinitions/${NAME}.yaml"
    echo "  touch ../../core/platform/bundles/paas/applicationdefinitions/${NAME}.yaml"
    echo "  touch ../../core/platform/bundles/naas/applicationdefinitions/${NAME}.yaml"
    echo "  touch ../../core/platform/bundles/system/applicationdefinitions/${NAME}.yaml"
  fi
  exit 1
fi

if [[ ! -s "$OUT" ]]; then
  cat >"$OUT" <<EOF
apiVersion: cozystack.io/v1alpha1
kind: ApplicationDefinition
metadata:
  name: ${NAME}
spec:
  release:
    values:
      _cozystack:
EOF
fi

# Determine package type (apps or extra) from current directory
CURRENT_DIR="$(pwd)"
PACKAGE_TYPE="apps"  # default
if [[ "$CURRENT_DIR" == *"/packages/extra/"* ]]; then
  PACKAGE_TYPE="extra"
elif [[ "$CURRENT_DIR" == *"/packages/apps/"* ]]; then
  PACKAGE_TYPE="apps"
fi

# Extract bundle type (iaas, paas, naas, system) from OUT path
OUT_DIR="$(dirname "$OUT")"
BUNDLE_DIR="$(dirname "$OUT_DIR")"
BUNDLE_TYPE="$(basename "$BUNDLE_DIR")"
ARTIFACT_PREFIX="cozystack-${BUNDLE_TYPE}"
ARTIFACT_NAME="${ARTIFACT_PREFIX}-${NAME}"

# Export vars for yq env()
export RES_NAME="$NAME"
# For packages/extra, prefix should be empty; for packages/apps, prefix is "${NAME}-"
if [[ "$PACKAGE_TYPE" == "extra" ]]; then
  export PREFIX=""
else
  export PREFIX="${NAME}-"
fi
export DESCRIPTION="$DESC"
export ICON_B64="$ICON_B64"
export ARTIFACT_NAME="$ARTIFACT_NAME"
export SCHEMA_JSON_MIN="$(jq -c . "$SCHEMA_JSON")"

# Generate keysOrder from values.yaml
export KEYS_ORDER="$(
  yq -o=json '.' "$VALUES_YAML" | jq -c '
    def get_paths_recursive(obj; path):
      obj | to_entries | map(
        .key as $key |
        .value as $value |
        if $value | type == "object" then
          [path + [$key]] + get_paths_recursive($value; path + [$key])
        else
          [path + [$key]]
        end
      ) | flatten(1)
    ;
    (
      [ ["apiVersion"], ["appVersion"], ["kind"], ["metadata"], ["metadata","name"] ]
    )
    +
    (
      get_paths_recursive(.; [])                  # get all paths in order
      | map(select(length>0))                     # drop root
      | map(map(select(type != "number")))        # drop array indices
      | map(["spec"] + .)                         # prepend "spec"
    )
  '
)"

# Remove lines with cozystack.build-values before updating (Helm template syntax breaks yq parsing)
if [[ -f "$OUT" && -n "$OUT" ]]; then
  # Use grep to filter out the line, more reliable than sed
  grep -v 'cozystack\.build-values' "$OUT" > "${OUT}.tmp" && mv "${OUT}.tmp" "$OUT"
fi

# Update only necessary fields in-place
# - openAPISchema is loaded from file as a multi-line string (block scalar)
# - labels ensure cozystack.io/ui: "true"
# - prefix = "<name>-"
# - sourceRef derived from directory (apps|extra)
yq -i '
  .apiVersion = (.apiVersion // "cozystack.io/v1alpha1") |
  .kind       = (.kind       // "ApplicationDefinition") |
  .metadata.name = strenv(RES_NAME) |
  .spec.application.openAPISchema = strenv(SCHEMA_JSON_MIN) |
  (.spec.application.openAPISchema style="literal") |
  .spec.release.prefix = (strenv(PREFIX)) |
  .spec.release.labels."cozystack.io/ui" = "true" |
  del(.spec.release.chart) |
  .spec.release.chartRef.sourceRef.kind = "ExternalArtifact" |
  .spec.release.chartRef.sourceRef.name = strenv(ARTIFACT_NAME) |
  .spec.release.chartRef.sourceRef.namespace = "cozy-system" |
  .spec.dashboard.description = strenv(DESCRIPTION) |
  .spec.dashboard.icon = strenv(ICON_B64) |
  .spec.dashboard.keysOrder = env(KEYS_ORDER)
' "$OUT"

# Add back the Helm template line after _cozystack
if [[ -f "$OUT" && -n "$OUT" ]]; then
  HELM_TEMPLATE='        {{- include "cozystack.build-values" . | nindent 8 }}'
  # Use awk for more reliable insertion
  awk -v template="$HELM_TEMPLATE" '/_cozystack:/ {print; print template; next} {print}' "$OUT" > "${OUT}.tmp" && mv "${OUT}.tmp" "$OUT"
fi

echo "Updated $OUT"
