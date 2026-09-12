#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# The definition closes on the indented brace below and runs a command on the
# same line; the column-0 brace at the end belongs to nothing. The range helper
# ends a body at the column-0 one, so the extracted text opens with a definition
# and ends with a bare `}`, and the middle line is neither -- it is a close
# followed by a command, which runs if that text is sourced.
cozy_wait_inner_brace_command() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  }; printf 'ran\n' >"${COZY_TAIL_WITNESS:-/dev/null}"
}
