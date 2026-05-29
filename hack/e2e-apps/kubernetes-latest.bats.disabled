#!/usr/bin/env bats

@test "Create a tenant Kubernetes control plane with latest version" {
  . hack/e2e-apps/run-kubernetes.sh
  # 4th arg = enable_ouroboros: folds the standalone ouroboros.bats's
  # hairpin-NAT reconciliation assertions onto this cluster instead of
  # spinning up a second ~25m Kamaji bringup.
  run_kubernetes_test 'keys | sort_by(.) | .[-1]' 'test-latest-version' '59991' 'true'
}
