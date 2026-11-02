#!/usr/bin/env bats

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
VMALERT_TOOL_IMAGE="victoriametrics/vmalert-tool:v1.136.0@sha256:417346d7b6fdb66d8e3984887c2ad114535ec7d983eeef53381b56c776f333f1"

@test "workload placement alerts pass their vmalert-tool cases" {
  docker info >/dev/null 2>&1 || {
    echo "docker is required to run the cases in $VMALERT_TOOL_IMAGE" >&2
    return 1
  }
  work=$(mktemp -d)
  rc=0
  yq '.spec' "$REPO_ROOT/packages/system/monitoring-agents/alerts/workload-placement.yaml" > "$work/rules.yaml" || rc=$?
  cp "$REPO_ROOT/hack/testdata/workload-placement-alerts/unittest.yaml" "$work/unittest.yaml" || rc=$?
  if [ "$rc" -eq 0 ]; then
    docker run --rm --network none -v "$work:/w:ro" "$VMALERT_TOOL_IMAGE" \
      unittest --files /w/unittest.yaml > "$work/out" 2>&1 || rc=$?
    cat "$work/out"
  fi
  passed=$(grep -cx 'SUCCESS' "$work/out" 2>/dev/null || true)
  rm -rf "$work"
  [ "$rc" -eq 0 ]
  [ "$passed" -eq 1 ]
}
