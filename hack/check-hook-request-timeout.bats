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
# Scope and its limit. A file that names KUBECONFIG is skipped: it hands kubectl
# an explicit kubeconfig, so the in-cluster branch is not what serves it and the
# flag is harmless there. That is a per-file judgement, not per-command, which
# is the coarse part of this check and is why it is written down rather than
# inferred. Vendored charts under packages/*/*/charts are upstream and out of
# scope.
#
# Bounding the call is still available: --timeout bounds a watch, and
# activeDeadlineSeconds bounds the whole Job, which is what
# packages/apps/tenant/templates/cleanup-job.yaml uses and documents.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"

@test "no in-cluster kubectl call carries a non-zero --request-timeout" {
  cd "$REPO_ROOT"

  # Files in scope: non-vendored templates that invoke kubectl.
  candidates="$(grep -rl 'kubectl' packages/*/*/templates/ 2>/dev/null || true)"
  [ -n "$candidates" ] || { echo "FAIL: found no templates invoking kubectl; this check is broken, not clean"; false; }

  # Guard against a vacuous pass: the tree ships many such templates, so a
  # collapsed glob has to fail here rather than report success.
  count="$(printf '%s\n' "$candidates" | grep -c . || true)"
  [ "$count" -ge 10 ] || { echo "FAIL: only $count template(s) invoke kubectl; expected the tree's usual population"; false; }

  offenders=""
  for f in $candidates; do
    # An explicit kubeconfig means the in-cluster branch is not in play here.
    if grep -q 'KUBECONFIG' "$f"; then
      continue
    fi
    hits="$(grep -nE -- '--request-timeout=[^0[:space:]]' "$f" || true)"
    if [ -n "$hits" ]; then
      offenders="${offenders}${f}
${hits}
"
    fi
  done

  if [ -n "$offenders" ]; then
    echo "FAIL: non-zero --request-timeout on a call that depends on in-cluster credentials."
    echo "It diverts kubectl to localhost:8080 and the call never reaches the cluster."
    echo "Bound the work with --timeout or activeDeadlineSeconds instead."
    printf '%s\n' "$offenders"
    false
  fi
}
