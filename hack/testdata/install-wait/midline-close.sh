#!/bin/sh
# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# The body closes in the MIDDLE of a line: the brace after `:;` ends the
# definition, and what follows on the same line stands outside it. No line here
# begins with a brace, so a refusal keyed on the first character of a line
# passes this range on and sources it -- which runs the command after the brace.
cozy_wait_midline_close() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  :; }; printf 'ran\n' >"${COZY_TAIL_WITNESS:-/dev/null}"
}
