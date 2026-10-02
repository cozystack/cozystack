#!/bin/sh
# Bodies for the nesting rule. Every function below writes the same wait the
# subject writes, so the identity matcher takes it in each of them; what
# differs is where the wait sits and whether the body parses at all.

cozy_wait_at_top() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  cozy_assert_tenant_reachable
}

cozy_wait_in_branch() {
  if [ "$mode" = None ]; then
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  fi
  cozy_assert_tenant_reachable
}

cozy_wait_in_loop() {
  for _attempt in 1 2 3; do
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  done
}

# Two waits on the same release: the printed body carries no line numbers, so
# the one the caller asked about cannot be told from the other.
cozy_wait_twice() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
}

cozy_wait_in_case() {
  case "$mode" in
  None)
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
    ;;
  esac
}

cozy_wait_in_until() {
  until [ "$ready" = yes ]; do
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  done
}

# A subshell prints on the line that opens it, so the wait is not at the start
# of any printed line and the command matcher does not see it at all.
cozy_wait_in_subshell() {
  ( kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  )
}

cozy_wait_none() {
  cozy_assert_tenant_reachable
}

cozy_wait_errexit_off() {
  set +e
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
}

# `set -euo pipefail` is what this tree writes, so `set +euo pipefail` is what
# an author writes to undo it: the `e` is not the last letter of the flag.
cozy_wait_errexit_combined() {
  set +euo pipefail
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
}

cozy_wait_errexit_after() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
  set +e
}

# Closes with a command on the brace line. The range helper ends a body at a
# `}` in column 0, so this reaches the extractor looking like a definition,
# and what stands after the brace runs if the text is sourced. The witness
# says whether it did; unset, it writes nowhere.
cozy_wait_tail() {
  kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
}; printf 'ran\n' >"${COZY_TAIL_WITNESS:-/dev/null}"

# Deliberately missing its `fi`, which is what a body truncated at a brace
# inside a heredoc looks like to the shell. Last in the file so the unbalanced
# `if` cannot be read as covering the definitions below it.
cozy_wait_broken() {
  if [ "$mode" = None ]; then
    kubectl wait hr -n tenant-test "kubernetes-${test_name}" --timeout=5m --for=condition=ready
}
