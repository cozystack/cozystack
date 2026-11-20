#!/usr/bin/env bash
set -euo pipefail
command=${1:?expected values, prepare or deadline}
bundle=${2:?expected pinned baseline bundle or job start}
if [ "$command" = deadline ]; then
  # The E2E job's timeout-minutes, less the reserve for the report and teardown
  # steps that run after the suites.
  jq --null-input --arg started "$bundle" '($started | fromdateiso8601) + (255 - 35) * 60'
  exit
fi
jq --exit-status '
  (.source | test("^[0-9a-f]{40}$")) and
  (.operatorImage | test("^[^[:space:]]+@sha256:[0-9a-f]{64}$")) and
  (.packagesURL | test("^oci://[^[:space:]]+$")) and
  (.packagesRef | test("^digest=sha256:[0-9a-f]{64}$"))
' "$bundle" >/dev/null
case "$command" in
  values)
    jq '{cozystackOperator:{image:.operatorImage,platformSourceUrl:.packagesURL,platformSourceRef:.packagesRef}}' "$bundle"
    ;;
  prepare)
    output=${3:?expected output directory}
    [ ! -e "$output" ] || { echo "baseline output already exists: $output" >&2; exit 1; }
    source=$(jq --raw-output .source "$bundle")
    git rev-parse --verify "${source}:packages/core/installer/Chart.yaml" >/dev/null
    mkdir --parents "$output"
    git archive --format=tar --output="$output/installer.tar" "$source" packages/core/installer packages/apps/kubernetes-nodes/values.yaml packages/apps/kubernetes-nodes/files/versions.yaml
    tar --extract --file="$output/installer.tar" --directory="$output"
    cp "$bundle" "$output/bundle.json"
    ;;
  *) echo "unsupported worker upgrade input command: $command" >&2; exit 1 ;;
esac
