#!/usr/bin/env bats
# Repo-wide invariant: a kubectl call that relies on in-cluster credentials must
# not carry a non-zero --request-timeout.
#
# client-go loads in-cluster credentials only while the assembled config still
# equals the default. --request-timeout lands in that config, so any non-zero
# value diverts the call to kubectl's built-in default cluster, localhost:8080,
# where it is refused. Nothing about this is visible in a rendered manifest: the
# flag reads as ordinary defensive practice, the chart renders, and the failure
# appears only when the container runs against a real apiserver. It cost two
# packages before it was found (#4276, #4288), and in one of them the hook
# reported success while doing nothing.
#
# Scope and its limit. Chart templates are checked, and so are shell scripts
# kept under packages/*/*/files/: a chart that loads its hook script with
# .Files.Get runs it with the same in-cluster credentials. Both the
# --request-timeout=<value> and the --request-timeout <value> forms are caught.
# A file that names KUBECONFIG is skipped: it hands kubectl
# an explicit kubeconfig, so the in-cluster branch is not what serves it and the
# flag is harmless there. That is a per-file judgement, not per-command, which
# is the coarse part of this check and is why it is written down rather than
# inferred. A script under files/ that gets KUBECONFIG from its template's env
# has no reason to name it, so a line passing a non-empty --context or
# --kubeconfig is skipped instead: client-go then serves the call from that
# kubeconfig or fails with "context was not found", never from localhost:8080.
# A literally empty value does fall back, so it is still reported; a variable
# that expands to nothing at runtime is not visible here, so guard it with
# ${VAR:?} as the shipped hooks do. The match is per
# line, so a flag in a trailing comment or in another command on the same line
# also exempts it. Vendored charts under packages/*/*/charts are upstream and
# out of scope.
#
# Bounding the call is still available: --timeout bounds a watch, and
# activeDeadlineSeconds bounds the whole Job, which is what
# packages/apps/tenant/templates/cleanup-job.yaml uses and documents.

load test_helper

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

# A non-zero value in either form, quoted or not. The space form needs a digit
# after it, so prose such as "a non-zero --request-timeout on a call" in a
# comment is not a hit; the = form keeps the rule it always had.
REQUEST_TIMEOUT_RE="--request-timeout(=[\"']?[^0[:space:]\"']|[[:space:]]+[\"']?[1-9])"

# is_shell_script FILE: a .sh file, or one whose first line is a sh/bash
# shebang, directly or through env, `env -S` included.
is_shell_script() {
  case "$1" in *.sh) return 0 ;; esac
  head -n 1 "$1" | grep -qE '^#!.*/(env([[:space:]]+-S)?[[:space:]]+)?(ba|da)?sh([[:space:]]|$)'
}

# request_timeout_candidates ROOT: non-vendored templates and files/ shell
# scripts under ROOT that invoke kubectl, one path per line.
request_timeout_candidates() {
  local root="$1" f
  grep -rl 'kubectl' "$root"/packages/*/*/templates/ 2>/dev/null || true
  for f in $(grep -rl 'kubectl' "$root"/packages/*/*/files/ 2>/dev/null || true); do
    if is_shell_script "$f"; then
      echo "$f"
    fi
  done
}

# request_timeout_offenders FILE...: the files, with matching lines, that pass
# a non-zero --request-timeout without naming KUBECONFIG or a non-empty
# --context or --kubeconfig.
request_timeout_offenders() {
  local f hits
  for f in "$@"; do
    # An explicit kubeconfig means the in-cluster branch is not in play here.
    if grep -q 'KUBECONFIG' "$f"; then
      continue
    fi
    hits="$(grep -nE -- "$REQUEST_TIMEOUT_RE" "$f" | grep -vE -- "--(context|kubeconfig)(=|[[:space:]]+)[\"']?[^\"'[:space:]]" || true)"
    if [ -n "$hits" ]; then
      printf '%s\n%s\n' "$f" "$hits"
    fi
  done
}

@test "no in-cluster kubectl call carries a non-zero --request-timeout" {
  cd "$REPO_ROOT"

  # Files in scope: non-vendored templates and files/ shell scripts that invoke kubectl.
  candidates="$(request_timeout_candidates .)"
  [ -n "$candidates" ] || { echo "FAIL: found no templates invoking kubectl; this check is broken, not clean"; false; }

  # Guard against a vacuous pass: the tree ships many such templates, so a
  # collapsed glob has to fail here rather than report success.
  count="$(printf '%s\n' "$candidates" | grep -c . || true)"
  [ "$count" -ge 10 ] || { echo "FAIL: only $count template(s) invoke kubectl; expected the tree's usual population"; false; }

  # shellcheck disable=SC2086 # paths in this tree carry no whitespace
  offenders="$(request_timeout_offenders $candidates)"

  if [ -n "$offenders" ]; then
    echo "FAIL: non-zero --request-timeout on a call that depends on in-cluster credentials."
    echo "It diverts kubectl to localhost:8080 and the call never reaches the cluster."
    echo "Bound the work with --timeout or activeDeadlineSeconds instead."
    printf '%s\n' "$offenders"
    false
  fi
}

# The cases below run the same scan on a throwaway tree, so they pin what it
# catches and what it leaves alone independently of the repository's content.
scan_tree() {
  # shellcheck disable=SC2046
  request_timeout_offenders $(request_timeout_candidates "$BATS_TEST_TMPDIR/tree")
}

write_tree_file() {
  mkdir -p "$(dirname "$BATS_TEST_TMPDIR/tree/$1")"
  printf '%s\n' "$2" > "$BATS_TEST_TMPDIR/tree/$1"
}

@test "a hook script under files/ is checked" {
  write_tree_file packages/x/y/files/hook.sh 'kubectl get pods --request-timeout=30s'
  [ -n "$(scan_tree)" ]
}

@test "a script under files/ without a .sh suffix is checked through its shebang" {
  write_tree_file packages/x/y/files/hook "$(printf '#!/bin/sh\nkubectl get pods --request-timeout=30s')"
  [ -n "$(scan_tree)" ]
}

@test "the space-separated form is caught" {
  write_tree_file packages/x/y/files/hook.sh 'kubectl get pods --request-timeout 30s'
  [ -n "$(scan_tree)" ]
  write_tree_file packages/x/y/files/hook.sh 'kubectl get pods --request-timeout=5'
  [ -n "$(scan_tree)" ]
}

@test "quoted values are caught" {
  write_tree_file packages/x/y/files/hook.sh 'kubectl get pods --request-timeout "30s"'
  [ -n "$(scan_tree)" ]
  write_tree_file packages/x/y/files/hook.sh "kubectl get pods --request-timeout='30s'"
  [ -n "$(scan_tree)" ]
}

@test "a script with an env -S shebang is checked" {
  write_tree_file packages/x/y/files/hook "$(printf '#!/usr/bin/env -S bash -e\nkubectl get pods --request-timeout=30s')"
  [ -n "$(scan_tree)" ]
}

@test "a script for another interpreter is not collected" {
  write_tree_file packages/x/y/files/hook "$(printf '#!/usr/bin/env -S python3\n# kubectl get pods --request-timeout=30s')"
  [ -z "$(scan_tree)" ]
}

@test "zero values, prose and kubeconfig users are left alone" {
  write_tree_file packages/x/y/files/hook.sh "$(printf 'kubectl get pods --request-timeout=0\nkubectl get pods --request-timeout 0s\nkubectl get pods --request-timeout "0"\n# a non-zero --request-timeout on a call is diverted')"
  write_tree_file packages/x/y/files/external.sh "$(printf 'export KUBECONFIG=/etc/kubeconfig\nkubectl get pods --request-timeout=30s')"
  write_tree_file packages/x/y/files/manifest.yaml 'command: ["kubectl", "get", "pods", "--request-timeout=30s"]'
  [ -z "$(scan_tree)" ]
}

@test "a call that names a kubeconfig context or file is left alone" {
  write_tree_file packages/x/y/files/hook.sh "$(printf '%s\n' \
    'timeout "$remaining" kubectl --context "$KUBE_CONTEXT" --namespace "$NAMESPACE" --request-timeout=10s "$@"' \
    'kubectl --kubeconfig=/etc/kubeconfig get pods --request-timeout 30s')"
  [ -z "$(scan_tree)" ]
}

@test "a bare call is still caught next to a call that names a context" {
  write_tree_file packages/x/y/files/hook.sh "$(printf '%s\n' \
    'kubectl --context "$KUBE_CONTEXT" get pods --request-timeout=10s' \
    'kubectl get pods --request-timeout=10s')"
  run scan_tree
  [ "${#lines[@]}" -eq 2 ]
  [ "${lines[1]}" = '2:kubectl get pods --request-timeout=10s' ]
}

@test "an empty context or kubeconfig value is still caught" {
  write_tree_file packages/x/y/files/hook.sh "$(printf '%s\n' \
    'kubectl --context "" get pods --request-timeout=10s' \
    'kubectl --kubeconfig= get pods --request-timeout=10s' \
    'kubectl --context= get pods --request-timeout=10s')"
  run scan_tree
  [ "${#lines[@]}" -eq 4 ]
}
