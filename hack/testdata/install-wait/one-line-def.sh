#!/bin/sh
# An assertion helper defined on one line at column 0, with a call site after
# it. The definition never reaches a `}` of its own, so a composition rule that
# opens a body on the definition line swallows the call below and answers that
# the script asserts nothing.

cozy_assert_one_line() { :; }

run_kubernetes_test() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  cozy_assert_tenant_reachable "${test_name}"
}
