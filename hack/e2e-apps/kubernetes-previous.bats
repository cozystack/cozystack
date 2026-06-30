#!/usr/bin/env bats

# Sourced at file scope (see kubernetes-latest.bats) so cozytest.sh's parent
# shell gets run_kubernetes_test + the cozy_cleanup() hook for the EXIT trap.
. hack/e2e-apps/run-kubernetes.sh
# TEMPORARY DIAGNOSTIC (debug/talos-bootstrap-timing) — DO NOT MERGE.
# Timestamped [TALOS-DEBUG] poller for tenant Talos worker-bootstrap timing.
. hack/e2e-apps/talos-debug-poller.bash

@test "Create a tenant Kubernetes control plane with previous version" {
  # TEMPORARY DIAGNOSTIC (debug/talos-bootstrap-timing) — DO NOT MERGE.
  # See kubernetes-latest.bats for the rationale; same poller wrapper.
  _talos_debug_poller_start 'test-previous-version'
  _k8s_rc=0
  run_kubernetes_test 'keys | sort_by(.) | .[-2]' 'test-previous-version' '59992' || _k8s_rc=$?
  if [ "${_k8s_rc}" -ne 0 ]; then
    echo "[TALOS-DEBUG] run_kubernetes_test exited rc=${_k8s_rc}; ~90s post-failure capture"
    sleep 90
  fi
  _talos_debug_poller_stop 'test-previous-version'
  [ "${_k8s_rc}" -eq 0 ]
}
