#!/usr/bin/env bats
# The operator's resync period is what bounds the stale-Ready window after an
# operator restart (kubernetes-sigs/cluster-api-operator#1031): about two
# periods. It reaches the manager only through patches/sync-period.diff, which
# `make update` has to reapply on every re-vendor, and through the value in
# values.yaml. Losing either silently returns the window to about 20 minutes,
# so this checks the argument the rendered Deployment actually carries.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
OPERATOR="$REPO_ROOT/packages/system/capi-operator"

@test "the operator manager runs with a one-minute resync period" {
  local rendered args
  rendered="$BATS_TEST_TMPDIR/operator.yaml"
  helm template capi-operator "$OPERATOR" --namespace cozy-cluster-api > "$rendered"
  args="$(yq -N 'select(.kind == "Deployment") | .spec.template.spec.containers[] | select(.name == "manager") | .args[]' "$rendered")"
  [ -n "$args" ] || { echo "no manager container args rendered from $OPERATOR" >&2; return 1; }
  case "$(printf '%s\n' "$args" | grep -c '^--sync-period=')" in
    1) ;;
    *) echo "expected exactly one --sync-period in the manager args, got:" >&2; echo "$args" >&2; return 1 ;;
  esac
  printf '%s\n' "$args" | grep -qx -- '--sync-period=1m' || { echo "manager args: $args" >&2; return 1; }
}
