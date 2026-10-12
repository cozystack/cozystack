#!/bin/sh
# The wrapper form with no `--timeout` at all. kubectl falls back to its own
# default, which is far below the floor the install needs, and the rule has to
# report a missing budget rather than exempt the line for its spelling.

run_kubernetes_test() {
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${test_name}
EOF
  kubectl_wait_retry hr -n tenant-test "kubernetes-${test_name}" --for=condition=ready
  cozy_assert_tenant_reachable "${test_name}"
}
