#!/bin/bash
set -eu

# Dev overlay: overlay working-tree changes onto a deployed OCI artifact.
#
# Usage: dev-overlay.sh <diff|apply>
#
# Required env vars:
#   REGISTRY            OCI registry to push to
# Optional env vars:
#   DEV_BASE_TAG        git ref to diff against (default: origin/main)
#   DEV_OVERLAY_TAG     tag for the overlay artifact (default: dev-overlay)
#   OPERATOR_DEPLOY     operator Deployment name (default: cozystack-operator)
#   OPERATOR_CONTAINER  container name in the Deployment (default: $OPERATOR_DEPLOY)
#   PLATFORM_OCIREPO    OCIRepository resource name (default: cozystack-platform)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"

DEV_BASE_TAG="${DEV_BASE_TAG:-origin/main}"
DEV_OVERLAY_TAG="${DEV_OVERLAY_TAG:-dev-overlay}"
OPERATOR_DEPLOY="${OPERATOR_DEPLOY:-cozystack-operator}"
OPERATOR_CONTAINER="${OPERATOR_CONTAINER:-$OPERATOR_DEPLOY}"
PLATFORM_OCIREPO="${PLATFORM_OCIREPO:-cozystack-platform}"

cluster_oci_url() {
  kubectl get ocirepository "$PLATFORM_OCIREPO" -n cozy-system \
    -o jsonpath='{.spec.url}' 2>/dev/null || true
}

resolve_cluster_pull_ref() {
  local url="$1"
  local tag digest
  tag=$(kubectl get ocirepository "$PLATFORM_OCIREPO" -n cozy-system \
    -o jsonpath='{.spec.ref.tag}' 2>/dev/null || true)
  digest=$(kubectl get ocirepository "$PLATFORM_OCIREPO" -n cozy-system \
    -o jsonpath='{.spec.ref.digest}' 2>/dev/null || true)

  if [ -n "$tag" ]; then
    echo "${url}:${tag}"
  elif [ -n "$digest" ]; then
    echo "${url}@${digest}"
  fi
}

pull_or_init_workdir() {
  local workdir="$1"
  local oci_url="$2"

  if flux pull artifact "oci://${REGISTRY}/cozystack-packages:${DEV_OVERLAY_TAG}" \
       --output "$workdir" 2>/dev/null; then
    echo "Existing overlay found, accumulating on top"
    return
  fi

  local base_pull
  base_pull=$(resolve_cluster_pull_ref "$oci_url")
  if [ -z "$base_pull" ]; then
    echo "ERROR: cluster OCIRepository has no tag or digest ref" >&2
    rm -rf "$workdir"
    exit 1
  fi

  echo "No overlay yet, pulling base from cluster: $base_pull"
  flux pull artifact "$base_pull" --output "$workdir"
}

apply_branch_changes() {
  local workdir="$1"
  local f rel

  # Copy changed/added/modified files from working tree
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    rel="${f#packages/}"
    mkdir -p "$workdir/$(dirname "$rel")"
    cp -r "$REPO_ROOT/$f" "$workdir/$rel"
  done < <(git -C "$REPO_ROOT" diff --name-only --diff-filter=ACMR \
      "${DEV_BASE_TAG}" -- packages/)

  # Copy untracked (new) files not yet known to git
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    rel="${f#packages/}"
    mkdir -p "$workdir/$(dirname "$rel")"
    cp -r "$REPO_ROOT/$f" "$workdir/$rel"
  done < <(git -C "$REPO_ROOT" ls-files --others --exclude-standard -- packages/)

  # Delete files removed relative to DEV_BASE_TAG
  while IFS= read -r f; do
    [ -z "$f" ] && continue
    rel="${f#packages/}"
    rm -rf "$workdir/$rel"
  done < <(git -C "$REPO_ROOT" diff --name-only --diff-filter=D \
      "${DEV_BASE_TAG}" -- packages/)

  # Delete old paths of renamed files to prevent duplicate Helm resources
  while IFS=$'\t' read -r _ old_path _; do
    [ -z "$old_path" ] && continue
    rel="${old_path#packages/}"
    rm -rf "$workdir/$rel"
  done < <(git -C "$REPO_ROOT" diff --diff-filter=R --name-status \
      "${DEV_BASE_TAG}" -- packages/)
}

push_artifact() {
  local workdir="$1"
  local logfile
  logfile=$(mktemp)
  trap 'rm -f "$logfile"' RETURN

  flux push artifact \
    "oci://${REGISTRY}/cozystack-packages:${DEV_OVERLAY_TAG}" \
    --path="$workdir" \
    --source=dev-overlay \
    --revision="dev:$(git -C "$REPO_ROOT" rev-parse HEAD)" \
    2>&1 | tee "$logfile" >&2

  local digest
  digest=$(awk -F @ '/artifact successfully pushed/ {print $2}' "$logfile")
  if [ -z "$digest" ]; then
    echo "ERROR: could not parse digest from push output" >&2
    exit 1
  fi
  echo "$digest"
}

patch_operator_ref() {
  local ref="$1"
  local url="$2"

  local jq_filter
  jq_filter=$(cat <<'JQ'
    .spec.template.spec.containers |= map(
      if .name == $container then
        .args |= map(
          if startswith("--platform-source-url=") then "--platform-source-url=" + $url
          elif startswith("--platform-source-ref=") then "--platform-source-ref=" + $ref
          else . end)
      else . end)
JQ
  )

  kubectl get deploy "$OPERATOR_DEPLOY" -n cozy-system -o json \
    | jq --arg ref "$ref" \
          --arg url "$url" \
          --arg container "$OPERATOR_CONTAINER" \
          "$jq_filter" \
    | kubectl apply -f -
}

cmd_diff() {
  local oci_url base_pull
  oci_url=$(cluster_oci_url)
  base_pull=$(resolve_cluster_pull_ref "$oci_url")
  : "${base_pull:=${oci_url}:${DEV_BASE_TAG}}"

  echo "Base: ${DEV_BASE_TAG}  Branch: $(git -C "$REPO_ROOT" rev-parse --abbrev-ref HEAD)"
  echo "Pull from: $base_pull"
  echo "Push to:   oci://${REGISTRY}/cozystack-packages:${DEV_OVERLAY_TAG}"

  local probe
  probe=$(mktemp -d)
  if flux pull artifact "oci://${REGISTRY}/cozystack-packages:${DEV_OVERLAY_TAG}" \
       --output "$probe" 2>/dev/null; then
    rm -rf "$probe"
    echo "Existing overlay found, changes will be layered on top"
  else
    rm -rf "$probe"
    echo "No overlay exists yet, $base_pull will be used as starting point"
  fi

  echo ""
  echo "=== Changed files (git diff) ==="
  git -C "$REPO_ROOT" diff --stat "${DEV_BASE_TAG}" -- packages/

  local untracked
  untracked=$(git -C "$REPO_ROOT" ls-files --others --exclude-standard -- packages/)
  if [ -n "$untracked" ]; then
    echo ""
    echo "=== Untracked new files ==="
    echo "$untracked"
  fi

  echo ""
  git -C "$REPO_ROOT" diff "${DEV_BASE_TAG}" -- packages/
}

cmd_apply() {
  local oci_url
  oci_url=$(cluster_oci_url)

  local workdir
  workdir=$(mktemp -d)
  trap "rm -rf '$workdir'" EXIT

  pull_or_init_workdir "$workdir" "$oci_url"
  apply_branch_changes "$workdir"

  local digest
  digest=$(push_artifact "$workdir")

  echo "Updating Deployment ${OPERATOR_DEPLOY} url=oci://${REGISTRY}/cozystack-packages ref=digest=${digest}"
  patch_operator_ref "digest=${digest}" "oci://${REGISTRY}/cozystack-packages"

  echo "Operator restarting — watch: kubectl rollout status deploy/${OPERATOR_DEPLOY} -n cozy-system"
}

case "${1:-}" in
  diff)  cmd_diff  ;;
  apply) cmd_apply ;;
  *)
    echo "Usage: $0 <diff|apply>" >&2
    exit 1
    ;;
esac
