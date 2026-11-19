#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# The file ends inside a backslash continuation, so the last command is still
# in the fold's buffer when the input runs out. Only the flush at EOF emits it;
# without that flush the wait below is never seen and the guard reports a
# script with no wait at all.
run_fixture_test() {
  cozy_assert_before "${test_name}"
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m \
    --for=condition=ready \