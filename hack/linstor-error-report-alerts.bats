#!/usr/bin/env bats
# Replays the LINSTOR error-report alerts from
# packages/system/piraeus-operator/alerts/piraeus-datastore.yaml through
# vmalert-tool, pinned to the VictoriaMetrics release that the
# victoria-metrics-operator deploys by default. Its increase() counts the first
# samples of a series as new when they are small, and the report counts are
# small, so the cases pin that a series which starts over after a controller
# move, a label change or a disconnect does not report old error reports again.
# Only pulling the image touches the network.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
VMALERT_TOOL_IMAGE="victoriametrics/vmalert-tool:v1.136.0@sha256:417346d7b6fdb66d8e3984887c2ad114535ec7d983eeef53381b56c776f333f1"

@test "LINSTOR error-report alerts pass their vmalert-tool cases" {
  docker info >/dev/null 2>&1 || {
    echo "docker is required to run the cases in $VMALERT_TOOL_IMAGE" >&2
    return 1
  }
  work=$(mktemp -d)
  yq '.spec' "$REPO_ROOT/packages/system/piraeus-operator/alerts/piraeus-datastore.yaml" > "$work/rules.yaml"
  cp "$REPO_ROOT/hack/testdata/linstor-error-report-alerts/unittest.yaml" "$work/unittest.yaml"
  rc=0
  docker run --rm --network none -v "$work:/w:ro" "$VMALERT_TOOL_IMAGE" \
    unittest --files /w/unittest.yaml > "$work/out" 2>&1 || rc=$?
  cat "$work/out"
  passed=$(grep -cx 'SUCCESS' "$work/out" || true)
  rm -rf "$work"
  [ "$rc" -eq 0 ]
  [ "$passed" -eq 1 ]
}
