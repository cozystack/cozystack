#!/usr/bin/env bash
set -euo pipefail

# Requirements: yq (v4), jq, base64
need() { command -v "$1" >/dev/null 2>&1 || { echo "need $1"; exit 1; }; }
need yq; need jq; need base64

CHART_YAML="${CHART_YAML:-Chart.yaml}"
VALUES_YAML="${VALUES_YAML:-values.yaml}"
SCHEMA_JSON="${SCHEMA_JSON:-values.schema.json}"
CRD_DIR="../../system/cozystack-resource-definitions/cozyrds"

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

# Decide which HelmRepository name to use based on path
#   .../apps/...  -> cozystack-apps
#   .../extra/... -> cozystack-extra
# default: cozystack-apps
SOURCE_NAME="cozystack-apps"
case "$PWD" in
  *"/apps/"*)  SOURCE_NAME="cozystack-apps" ;;
  *"/extra/"*) SOURCE_NAME="cozystack-extra" ;;
esac

# If file doesn't exist, create a minimal skeleton
OUT="${OUT:-$CRD_DIR/$NAME.yaml}"
if [[ ! -f "$OUT" ]]; then
  cat >"$OUT" <<EOF
apiVersion: cozystack.io/v1alpha1
kind: CozystackResourceDefinition
metadata:
  name: ${NAME}
spec: {}
EOF
fi

# Export vars for yq env()
export RES_NAME="$NAME"
export PREFIX="$NAME-"
if [ "$SOURCE_NAME" == "cozystack-extra" ]; then
  export PREFIX=""
fi
export DESCRIPTION="$DESC"
export ICON_B64="$ICON_B64"
export SOURCE_NAME="$SOURCE_NAME"
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

# Update only necessary fields in-place
# - openAPISchema is loaded from file as a multi-line string (block scalar)
# - labels ensure cozystack.io/ui: "true"
# - prefix = "<name>-"
# - sourceRef derived from directory (apps|extra)
yq -i '
  .apiVersion = (.apiVersion // "cozystack.io/v1alpha1") |
  .kind       = (.kind       // "CozystackResourceDefinition") |
  .metadata.name = strenv(RES_NAME) |
  .spec.application.openAPISchema = strenv(SCHEMA_JSON_MIN) |
  (.spec.application.openAPISchema style="literal") |
  .spec.release.prefix = (strenv(PREFIX)) |
  .spec.release.labels."cozystack.io/ui" = "true" |
  .spec.release.chart.name = strenv(RES_NAME) |
  .spec.release.chart.sourceRef.kind = "HelmRepository" |
  .spec.release.chart.sourceRef.name = strenv(SOURCE_NAME) |
  .spec.release.chart.sourceRef.namespace = "cozy-public" |
  .spec.dashboard.description = strenv(DESCRIPTION) |
  .spec.dashboard.icon = strenv(ICON_B64) |
  .spec.dashboard.keysOrder = env(KEYS_ORDER)
' "$OUT"

echo "Updated $OUT"
