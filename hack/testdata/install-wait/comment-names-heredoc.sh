#!/bin/sh
# A `<<WORD` that exists only inside a trailing comment, before the real wait.
# Nothing opens a heredoc here. Read the line without cutting the comment first
# and the probe takes `<<PATCHDOC` for an opener, then swallows every line until
# a lone `PATCHDOC` that never comes -- the wait below among them -- and the
# answer is "no wait" on a script that waits.
#
# This is the input that tells whether the fold cuts comments before looking for
# an opener. The fixture that names a heredoc inside a comment elsewhere does
# not: its own answer is the same either way.

run_kubernetes_test() {
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kubernetes
metadata:
  name: ${test_name}
EOF
  echo "the patch form is written with a heredoc"   # like `kubectl patch <<PATCHDOC`
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready # @wait
  cozy_assert_tenant_reachable "${test_name}"
}
