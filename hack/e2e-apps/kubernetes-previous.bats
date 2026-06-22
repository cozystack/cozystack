#!/usr/bin/env bats

# Sourced at file scope (see kubernetes-latest.bats) so cozytest.sh's parent
# shell gets run_kubernetes_test + the cozy_cleanup() hook for the EXIT trap.
. hack/e2e-apps/run-kubernetes.sh

@test "Create a tenant Kubernetes control plane with previous version" {
  run_kubernetes_test 'keys | sort_by(.) | .[-2]' 'test-previous-version' '59992'
}
