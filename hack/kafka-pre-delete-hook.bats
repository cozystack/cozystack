#!/usr/bin/env bats
# Behavioural tests for the Kafka pre-delete hook:
#
#   packages/apps/kafka/templates/hooks/delete.yaml
#
# The script is extracted from the rendered chart and RUN against a fake
# kubectl, because every property worth pinning here is an EXIT CODE and no
# amount of matching against the manifest text establishes one. The
# helm-unittest suite in packages/apps/kafka/tests/delete_test.yaml matches the
# source text instead, and stayed green through a mutation that put the timeout
# branch back to `exit 1` — which is the whole subject of this file.
#
# Why the exit code carries so much: a non-zero hook returns from Uninstall.Run
# before any release resource goes, so Helm leaves the topic operator running
# and helm-controller retries. That is the difference between "the uninstall
# waits" and "the topic operator is gone while topics still hold
# strimzi.io/topic-operator", which is issue #3793.
#
# What is pinned here:
#   * a delete that succeeds exits 0;
#   * a CRD that is absent is not a failure: the release may never have created
#     a topic, and the uninstall must not wedge on that;
#   * a wait that timed out exits 0 DELIBERATELY, so the uninstall can finish,
#     and says so on stderr. This is the branch 2b47c5a5 added; reverting it
#     must turn this file red;
#   * any OTHER kubectl failure exits non-zero, so a Forbidden or an apiserver
#     error cannot be mistaken for the timeout case and silently proceed;
#   * the selector names this release only.
#
# Run via hack/cozytest.sh from the repo root (make bats-unit-tests); relative
# paths resolve against that cwd. That runner implements `@test` and little
# else: it has NO `run` helper and therefore no $status or $output, so every
# test below saves `$?` by hand the way hack/app-cleanup-hooks_test.bats does.
# Under the bats binary `run` would work and the file would pass either way,
# which is exactly how a suite that dies on `run: command not found` under the
# real runner got committed once. docs/agents/e2e-testing.md:66 says it too.
# Each @test builds its own fixture and removes it in the body: no
# setup/teardown, and no EXIT trap.

KAFKA_CHART=packages/apps/kafka

# render_hook <chart> <release> <namespace> <job-name> <out>
# Extract the named Job's shell script from the rendered chart into <out>. The
# non-empty check must be an early `return 1`: hack/cozytest.sh rewrites every
# line matching ^}$ into `return 0` plus `}`, helpers included, so a check left
# in last-command position has its status discarded.
render_hook() {
  chart=$1 release=$2 namespace=$3 job=$4 out=$5
  helm template "$release" "$chart" --namespace "$namespace" \
    | yq eval "select(.kind == \"Job\" and .metadata.name == \"$job\") | .spec.template.spec.containers[0].command[2]" - > "$out"
  [ -s "$out" ] || return 1
}

# write_fake_kubectl <dir>
# Emit a fake `kubectl` that logs its call to $KLOG and replays whatever
# $DELETE_OUT holds, exiting with $DELETE_RC. The hook reads kubectl's combined
# output and branches on its text, so the fixture is that text verbatim: the
# timeout string is what kubectl v1.32 prints from the wait path, and the
# CRD-absent strings are the two RESTMapper wordings.
# DELETE_RC deliberately has NO default: an unset value would make every
# failure-path test exit 0 and assert nothing. Each test sets it.
# No line below is a bare column-0 `}`, so cozytest.sh's parser leaves it alone.
write_fake_kubectl() {
  cat > "$1/kubectl" <<'KEOF'
#!/bin/sh
echo "$*" >> "$KLOG"
if [ -z "${DELETE_RC:-}" ]; then
  echo "fake kubectl: DELETE_RC unset; the test would assert nothing" >&2
  exit 64
fi
printf '%s\n' "${DELETE_OUT:-kafkatopic.kafka.strimzi.io \"t\" deleted}"
exit "$DELETE_RC"
KEOF
  chmod +x "$1/kubectl"
}

@test "a render that produces no script fails instead of passing as a no-op" {
  # Guards the guard: every test below runs whatever render_hook produced, so an
  # empty render would satisfy the exit-0 assertions without executing a line of
  # the hook.
  tmp=$(mktemp -d)
  if render_hook "$KAFKA_CHART" kafka-test tenant-test no-such-job "$tmp/hook.sh"; then
    echo "FAIL: render_hook reported success for a Job name the chart never emits"
    false
  fi
  rm -rf "$tmp"
}

@test "kafka pre-delete hook exits 0 and deletes this release's topics only" {
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$KAFKA_CHART" kafka-test tenant-test kafka-test-pre-delete "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export DELETE_RC=0
  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc on a clean delete"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q "strimzi.io/cluster=kafka-test" "$tmp/calls"; then
    echo "FAIL: the delete was not scoped to this release"
    cat "$tmp/calls"
    false
  fi
  if ! grep -q -- "--ignore-not-found=true" "$tmp/calls"; then
    echo "FAIL: --ignore-not-found is gone, so an already-deleted topic fails the hook"
    cat "$tmp/calls"
    false
  fi
  rm -rf "$tmp"
}

@test "kafka pre-delete hook exits 0 when the KafkaTopic CRD is absent" {
  # A release that never created a topic, or a cluster where Strimzi is already
  # gone. Failing here would wedge the uninstall over nothing to clean up. Both
  # RESTMapper wordings are covered because kubectl picks between them.
  for msg in 'error: the server doesn'"'"'t have a resource type "kafkatopics"' \
             'error: no matches for kind "KafkaTopic" in version "kafka.strimzi.io/v1beta2"'; do
    tmp=$(mktemp -d)
    write_fake_kubectl "$tmp"
    render_hook "$KAFKA_CHART" kafka-test tenant-test kafka-test-pre-delete "$tmp/hook.sh"

    export KLOG="$tmp/calls"
    export DELETE_RC=1
    export DELETE_OUT="$msg"
    rc=0
    PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
    if [ "$rc" -ne 0 ]; then
      echo "FAIL: hook exited $rc on an absent CRD, wedging the uninstall"
      cat "$tmp/out" "$tmp/err"
      false
    fi
    if ! grep -q 'CRD absent' "$tmp/out"; then
      echo "FAIL: the absent CRD was not reported as such"
      cat "$tmp/out"
      false
    fi
    rm -rf "$tmp"
  done
}

@test "kafka pre-delete hook exits 0 on a wait timeout and says the finalizer is still held" {
  # The branch 2b47c5a5 added. Exiting non-zero here wedges the uninstall
  # forever, because helm-controller retries it; exiting 0 lets the uninstall
  # finish and leaves the topics for a human. Reverting that branch to `exit 1`
  # turns this test red, which is the point: the helm-unittest suite does not
  # notice.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$KAFKA_CHART" kafka-test tenant-test kafka-test-pre-delete "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export DELETE_RC=1
  export DELETE_OUT='error: timed out waiting for the condition on kafkatopics/kafka-test-orders'
  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "FAIL: hook exited $rc on a wait timeout, which wedges the uninstall"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q 'still hold the strimzi.io/topic-operator finalizer' "$tmp/err"; then
    echo "FAIL: the degraded path said nothing about the finalizer"
    cat "$tmp/err"
    false
  fi
  if ! grep -q 'clear the finalizer by hand' "$tmp/err"; then
    echo "FAIL: the degraded path did not say what to do about it"
    cat "$tmp/err"
    false
  fi
  rm -rf "$tmp"
}

@test "kafka pre-delete hook exits non-zero on any other kubectl failure" {
  # A Forbidden, an unreachable apiserver or a webhook rejection must NOT be
  # read as the timeout case. Here the non-zero exit is the safety net: Helm
  # stops before deleting the topic operator, so the topics keep something that
  # can clear their finalizer.
  tmp=$(mktemp -d)
  write_fake_kubectl "$tmp"
  render_hook "$KAFKA_CHART" kafka-test tenant-test kafka-test-pre-delete "$tmp/hook.sh"

  export KLOG="$tmp/calls"
  export DELETE_RC=1
  export DELETE_OUT='Error from server (Forbidden): kafkatopics.kafka.strimzi.io is forbidden'
  rc=0
  PATH="$tmp:$PATH" sh "$tmp/hook.sh" > "$tmp/out" 2> "$tmp/err" || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL: hook exited 0 on a Forbidden, so Helm would remove the topic operator"
    cat "$tmp/out" "$tmp/err"
    false
  fi
  if ! grep -q '^ERROR: deleting KafkaTopics failed' "$tmp/err"; then
    echo "FAIL: the failure was not reported as an error"
    cat "$tmp/err"
    false
  fi
  rm -rf "$tmp"
}
