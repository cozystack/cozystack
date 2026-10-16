#!/usr/bin/env bats
# Behavioural tests for the node-pool hook of packages/apps/kafka. The shell
# script is extracted from the rendered Job and executed against a stub
# kubectl, so what the hook actually applies, and whether a failed apply fails
# the Helm upgrade, are exercised for real. The helm-unittest cases in
# packages/apps/kafka/tests/migration-hook_test.yaml only match the script's
# source TEXT.
#
# Run from the repo root with make bats-unit-tests. Direct assertions and
# inline cleanup retain compatibility with cozytest. Its translator rewrites
# lone closing braces even inside heredocs, so the stub avoids shell functions
# and helper closing braces stay indented.

load test_helper

CHART=packages/apps/kafka

render_script() {
  helm template test-kafka "$CHART" \
    --namespace tenant-test \
    --show-only templates/migration-hook.yaml 2>"$1.err" |
    yq 'select(.kind == "Job") | .spec.template.spec.containers[0].args[0]' - > "$1"
  if [ ! -s "$1" ]; then
    echo "FAIL: the chart rendered no node-pool Job script" >&2
    grep -v 'found symbolic link in path' "$1.err" >&2 || true
    return 1
  fi
  }

make_bin() {
  mkdir -p "$1"
  cat > "$1/kubectl" <<'STUB'
#!/bin/sh
echo "$*" >> "${KUBECTL_LOG:-/dev/null}"
case "$*" in
  *"apply "*"-f -"*)
    cat >> "${KUBECTL_LOG:-/dev/null}"
    [ -n "${STUB_APPLY_FAIL:-}" ] && { echo "apiserver error" >&2; exit 1; }
    exit 0
    ;;
esac
exit 0
STUB
  chmod 0755 "$1/kubectl"
  }

setup_case() {
  tmp=$(mktemp -d) || return 1
  render_script "$tmp/s.sh" || return 1
  make_bin "$tmp/bin" || return 1
  }

run_hook() {
  rc=0
  : > "$tmp/kubectl.log"
  KUBECONFIG=/dev/null \
  KUBECTL_LOG="$tmp/kubectl.log" \
  STUB_APPLY_FAIL="${STUB_APPLY_FAIL:-}" \
  PATH="$tmp/bin:$PATH" sh "$tmp/s.sh" > "$tmp/out" 2>&1 || rc=$?
  [ -s "$tmp/kubectl.log" ] || return 1
  }

@test "the hook applies the broker and the controller pool through the v1 API" {
  setup_case
  run_hook

  [ "$rc" = 0 ]
  applies=$(grep -c 'apply --openapi-patch=false -f -' "$tmp/kubectl.log")
  [ "$applies" = 2 ]
  pools=$(grep -c '^apiVersion: kafka.strimzi.io/v1$' "$tmp/kubectl.log")
  [ "$pools" = 2 ]
  grep -qE '^ *- broker$' "$tmp/kubectl.log"
  grep -qE '^ *- controller$' "$tmp/kubectl.log"
  if grep -qE 'v1beta2|strimzi.io/kraft|KafkaRebalance|(^| )(annotate|delete|patch|create) ' "$tmp/kubectl.log"; then
    echo "FAIL: the hook did more than apply the two pools"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}

@test "a failed pool apply fails the hook" {
  setup_case
  STUB_APPLY_FAIL=1 run_hook

  [ "$rc" != 0 ]
  applies=$(grep -c 'apply --openapi-patch=false -f -' "$tmp/kubectl.log")
  if [ "$applies" != 1 ]; then
    echo "FAIL: the hook went on after the broker pool apply failed ($applies applies)"
    cat "$tmp/kubectl.log"
    return 1
  fi

  rm -rf "$tmp"
}
