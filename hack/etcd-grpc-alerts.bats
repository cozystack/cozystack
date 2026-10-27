#!/usr/bin/env bats

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
VMALERT_TOOL_IMAGE="victoriametrics/vmalert-tool:v1.136.0@sha256:417346d7b6fdb66d8e3984887c2ad114535ec7d983eeef53381b56c776f333f1"

@test "tenant etcd failed gRPC request alerts pass their vmalert-tool cases" {
  docker info >/dev/null 2>&1 || {
    echo "docker is required to run the cases in $VMALERT_TOOL_IMAGE" >&2
    return 1
  }
  work=$(mktemp -d)
  helm template etcd "$REPO_ROOT/packages/extra/etcd" --namespace tenant-test \
    --set '_cluster.cluster-domain=cozy.local' \
    --show-only templates/prometheus-rules.yaml > "$work/rendered.yaml"
  yq '.spec' "$work/rendered.yaml" > "$work/rules.yaml"
  cp "$REPO_ROOT/hack/testdata/etcd-grpc-alerts/unittest.yaml" "$work/unittest.yaml"
  rc=0
  docker run --rm --network none -v "$work:/w:ro" "$VMALERT_TOOL_IMAGE" \
    unittest --files /w/unittest.yaml > "$work/out" 2>&1 || rc=$?
  cat "$work/out"
  passed=$(grep -cx 'SUCCESS' "$work/out" || true)
  rm -rf "$work"
  [ "$rc" -eq 0 ]
  [ "$passed" -eq 1 ]
}
