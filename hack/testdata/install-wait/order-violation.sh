#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# The only shape that produces the "before" verdict: every call in the same
# body as the wait, one of them ahead of it. Why the sibling fixture cannot
# produce it, and why the live script cannot either, is at _ordering_violation
# in the guard, where the precedence is decided.
violating_body() {
  cozy_assert_early "${test_name}" # @early-call
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready # @wait
  cozy_assert_late "${test_name}"
}
