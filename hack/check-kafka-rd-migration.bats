#!/usr/bin/env bats
# The ZK->KRaft pre-upgrade migration Job runs for tens of minutes (broker
# rolling restarts through the Strimzi state machine). helm-controller waits on
# the pre-upgrade hook within the HelmRelease timeout, which defaults to 10m.
# The kafka ApplicationDefinition must raise it above the Job's self-deadline,
# or a real migration is aborted at 10m and thrashed on every remediation.

REPO_ROOT="$(cd "$(dirname "${BATS_TEST_FILENAME:-$0}")/.." && pwd)"
COZYRDS="$REPO_ROOT/packages/system/kafka-rd/cozyrds/kafka.yaml"
HOOK="$REPO_ROOT/packages/apps/kafka/templates/migration-hook.yaml"

@test "kafka-rd declares a helm-install-timeout for the migration" {
  grep -q "release.cozystack.io/helm-install-timeout" "$COZYRDS"
}

@test "kafka-rd helm-install-timeout is above the 10m default" {
  val=$(grep -oE 'helm-install-timeout: "[0-9]+m"' "$COZYRDS" | grep -oE '[0-9]+')
  [ -n "$val" ]
  [ "$val" -gt 10 ]
}

@test "migration Job activeDeadlineSeconds stays below the helm-install-timeout" {
  timeout_min=$(grep -oE 'helm-install-timeout: "[0-9]+m"' "$COZYRDS" | grep -oE '[0-9]+')
  deadline_s=$(grep -oE 'activeDeadlineSeconds: [0-9]+' "$HOOK" | grep -oE '[0-9]+')
  [ -n "$timeout_min" ]
  [ -n "$deadline_s" ]
  # The release timeout (converted to seconds) must exceed the Job's own
  # deadline, so helm-controller does not kill the operation before the Job can.
  [ "$((timeout_min * 60))" -gt "$deadline_s" ]
}
