# Fixture for hack/run-kubernetes-install-wait_test.bats.
# Not executed: read as text by the guard's matchers.
#
# Every shape here is a heredoc whose body contains something that LOOKS like a
# terminator but is not one, followed by a fully qualifying wait. The shell
# keeps reading data past each of them, so none of these waits ever runs; a
# matcher that ends the heredoc early hands the wait back as a command and
# answers with a line from a YAML document.#
# `git diff --check` flags the trailing space on that line, deliberately:
# the space is the subject here, not an accident, and a case reds if it is
# stripped. Nothing suppresses the warning -- a suppression would cover the
# next file written here too, where a stray space would be an accident.
creating_body() {
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: KubernetesNodes
spec:
  script: |
    EOF
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
EOF
  kubectl apply -f - <<EOF
data:
  note: |
EOF 
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=6m --for=condition=ready
EOF
  kubectl apply -f - <<-TABBED
	data: |
      TABBED
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=7m --for=condition=ready
	TABBED
  # Two openers on one command. Their bodies arrive in the order opened, so
  # FIRST's terminator ends the first body and the wait below it is still data,
  # inside SECOND. Tracking one delimiter per command ends the tracking here and
  # hands that wait back as a command.
  kubectl apply -f - <<FIRST <<SECOND
data: one
FIRST
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=8m --for=condition=ready
SECOND
  cozy_assert_after "${test_name}"
}
