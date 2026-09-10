#!/bin/sh
# Render app charts on their defaults plus injected cluster/namespace values.
# helm-unittest renders only templates selected by a suite; this smoke check
# also reaches templates with no assertions. It checks rendering, not Kubernetes
# schema validity or runtime behavior. See testdata/render-fixtures/README.md.
# Usage (from the repository root): hack/check-render-matrix.sh [chart-dir ...]
set -eu

FIXTURE_DIR="hack/testdata/render-fixtures"

# Empty lookup results are the only accepted reason to skip a chart. Match the
# actual diagnostic, so another error cannot keep an obsolete skip alive.
skip_reason() {
  case "$1" in
    vm-instance) echo 'Specified instanceType does not exist in the cluster: u1.medium' ;;
    kubernetes-nodes) echo 'specified instanceType "u1.medium" not found in cluster' ;;
    *) echo "" ;;
  esac
}

render_chart() {
  chart=$1 fixture=$2
  name=$(basename "$chart")
  case "$name" in
    tenant) set -- tenant-sub ;;
    # Satisfy the parent-cluster and release-name contract before the lookup.
    kubernetes-nodes) set -- kubernetes-nodes-render-check --set-string cluster=render ;;
    *) set -- "$name-render-check" ;;
  esac
  helm template "$@" "$chart" -n tenant-test -f "$fixture"
}

if [ "$#" -eq 0 ]; then
  set -- packages/apps/*/
  if [ ! -d "$1" ]; then
    echo "check-render-matrix: found no charts under packages/apps" >&2
    exit 1
  fi
fi

# Check exactly the files the render loop will consume, not nested fixtures.
fixtures=0
for fixture in "$FIXTURE_DIR"/*.yaml; do
  [ -f "$fixture" ] || continue
  fixtures=$((fixtures + 1))
done
if [ "$fixtures" -eq 0 ]; then
  echo "check-render-matrix: no fixtures under $FIXTURE_DIR" >&2
  exit 1
fi

errors=$(mktemp)
trap 'rm -f "$errors"' EXIT
rc=0 rendered=0 skipped=0
for dir in "$@"; do
  name=$(basename "$dir")
  reason=$(skip_reason "$name")
  chart_failed=0
  for fixture in "$FIXTURE_DIR"/*.yaml; do
    [ -f "$fixture" ] || continue
    state=$(basename "$fixture" .yaml)
    if render_chart "$dir" "$fixture" >/dev/null 2>"$errors"; then
      if [ -z "$reason" ]; then
        continue
      fi
      echo "FAIL $name ($state): chart now renders; remove its skip"
    else
      if [ -n "$reason" ] && grep -Fq "$reason" "$errors"; then
        continue
      fi
      echo "FAIL $name ($state)"
      sed -n '/^level=INFO/d; s/^/    /; p' "$errors"
    fi
    rc=1
    chart_failed=1
  done
  [ "$chart_failed" -eq 0 ] || continue
  if [ -n "$reason" ]; then
    echo "SKIP $name ($reason)"
    skipped=$((skipped + 1))
  else
    rendered=$((rendered + 1))
  fi
done

echo "check-render-matrix: $rendered rendered, $skipped skipped"
exit "$rc"
