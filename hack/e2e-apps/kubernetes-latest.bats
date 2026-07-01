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

# Version-independent, so it runs once here (not in kubernetes-previous.bats).
# Does not provision a tenant cluster -- it renders the kubernetes chart against
# the live management cluster -- so it is kept separate from the heavy tenant
# test above and runs regardless of that test's outcome.
@test "Tenant default StorageClass is never the kubevirt alias (PR #2872 B1 regression)" {
  verify_storageclass_fallback_default
}
