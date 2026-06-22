#!/usr/bin/env bats

# Sourced at file scope (not inside the @test) so cozytest.sh sources it into
# the parent shell: both run_kubernetes_test and the cozy_cleanup() hook the
# EXIT trap calls must live there (the @test itself runs in a subshell).
. hack/e2e-apps/run-kubernetes.sh

@test "Create a tenant Kubernetes control plane with latest version" {
  # 4th arg = enable_ouroboros: folds the standalone ouroboros.bats's
  # hairpin-NAT reconciliation assertions onto this cluster instead of
  # spinning up a second ~25m Kamaji bringup.
  run_kubernetes_test 'keys | sort_by(.) | .[-1]' 'test-latest-version' '59991' 'true'
}
