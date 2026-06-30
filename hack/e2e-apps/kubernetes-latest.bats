#!/usr/bin/env bats

# Sourced at file scope (not inside the @test) so cozytest.sh sources it into
# the parent shell: both run_kubernetes_test and the cozy_cleanup() hook the
# EXIT trap calls must live there (the @test itself runs in a subshell).
. hack/e2e-apps/run-kubernetes.sh
# TEMPORARY DIAGNOSTIC (debug/talos-bootstrap-timing) — DO NOT MERGE.
# Timestamped [TALOS-DEBUG] poller for tenant Talos worker-bootstrap timing.
. hack/e2e-apps/talos-debug-poller.bash

@test "Create a tenant Kubernetes control plane with latest version" {
  # 4th arg = enable_ouroboros: folds the standalone ouroboros.bats's
  # hairpin-NAT reconciliation assertions onto this cluster instead of
  # spinning up a second ~25m Kamaji bringup.
  #
  # TEMPORARY DIAGNOSTIC (debug/talos-bootstrap-timing) — DO NOT MERGE.
  # Run the [TALOS-DEBUG] timing poller in the background across the whole
  # bringup, capture run_kubernetes_test's outcome without aborting, give the
  # poller a short post-failure tail (to see whether the TCT lands just after
  # the md0 wait times out), stop the poller, then propagate the result.
  _talos_debug_poller_start 'test-latest-version'
  _k8s_rc=0
  run_kubernetes_test 'keys | sort_by(.) | .[-1]' 'test-latest-version' '59991' 'true' || _k8s_rc=$?
  if [ "${_k8s_rc}" -ne 0 ]; then
    echo "[TALOS-DEBUG] run_kubernetes_test exited rc=${_k8s_rc}; ~90s post-failure capture"
    sleep 90
  fi
  _talos_debug_poller_stop 'test-latest-version'
  [ "${_k8s_rc}" -eq 0 ]
}

# Version-independent, so it runs once here (not in kubernetes-previous.bats).
# Does not provision a tenant cluster -- it renders the kubernetes chart against
# the live management cluster -- so it is kept separate from the heavy tenant
# test above and runs regardless of that test's outcome.
@test "Tenant default StorageClass is never the kubevirt alias (PR #2872 B1 regression)" {
  verify_storageclass_fallback_default
}
