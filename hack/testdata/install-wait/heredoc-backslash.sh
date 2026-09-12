#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# A backslash quotes the delimiter exactly as a quote does: `<<\EOF` opens a
# heredoc whose body is data with no expansion. An opener matcher that takes
# the quotes and not the backslash reads this body as commands, and the wait
# written into it below is data that never runs.
creating_body() {
  kubectl apply -f - <<\EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
spec:
  script: |
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
EOF
  cozy_assert_after "${test_name}"
}
