#!/bin/sh
# A heredoc whose opening line carries a quoted ` #` before the `<<`. A crude
# comment cut loses the opener, the body is read as commands, and the planted
# line below -- data on its way to `kubectl apply` -- is answered as the
# readiness wait, on a script that never waits.

run_kubernetes_test() {
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${test_name}
EOF
  kubectl annotate ns x "note #1" --overwrite && kubectl apply -f - <<YAML
data:
  planted: |
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
YAML
  cozy_assert_tenant_reachable "${test_name}"
}
