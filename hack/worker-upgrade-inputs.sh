#!/usr/bin/env bash
set -euo pipefail
command=${1:?expected values, prepare or deadline}
bundle=${2:?expected pinned baseline bundle or job start}
if [ "$command" = deadline ]; then
  # The E2E job's timeout-minutes, less the reserve for the report and teardown
  # steps that run after the suites.
  jq --null-input --arg started "$bundle" '($started | fromdateiso8601) + (305 - 35) * 60'
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
    # A baseline the candidate does not contain makes the upgrade roll back
    # whatever landed between the two, so the lane would judge a downgrade.
    # Clusters a baseline without per-tenant etcd creates run on the shared
    # tenant-root DataStore, which the e2e install no longer provides.
    git cat-file -e "${source}:packages/apps/kubernetes/templates/etcd.yaml" 2>/dev/null || {
      echo "baseline source $source predates per-tenant etcd (no packages/apps/kubernetes/templates/etcd.yaml)" >&2
      exit 1
    }
    git merge-base --is-ancestor "$source" HEAD || {
      echo "baseline source $source is not an ancestor of the candidate $(git rev-parse HEAD)" >&2
      exit 1
    }
    mkdir --parents "$output"
    git archive --format=tar --output="$output/installer.tar" "$source" packages/core/installer packages/apps/kubernetes/values.yaml packages/apps/kubernetes-nodes/values.yaml packages/apps/kubernetes-nodes/files/versions.yaml
    tar --extract --file="$output/installer.tar" --directory="$output"
    cp "$bundle" "$output/bundle.json"
    ;;
  *) echo "unsupported worker upgrade input command: $command" >&2; exit 1 ;;
esac
