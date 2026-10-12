#!/bin/sh
# A subject whose readiness wait goes through the retry wrapper the e2e library
# defines for transient apiserver failures. Why that form is placed and budgeted
# like a direct wait is written once, beside WAIT_COMMAND_RE in the guard.

run_kubernetes_test() {
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${test_name}
EOF
  kubectl_wait_retry hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  cozy_assert_tenant_reachable "${test_name}"
}
