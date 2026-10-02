#!/usr/bin/env bats
# The central traces collector reads its vmauth password at start and is rolled
# by a checksum of it. On a first install the password is random, and it is
# rendered twice, into the Secret and into that checksum: if the two renders
# disagree, the collector rolls on every reconcile and the checksum proves
# nothing. helm-unittest asserts one template at a time and cannot compare them,
# so this renders both together and compares.
#
# Run from the repo root: bats hack/tracing-central-credentials.bats.

load test_helper

MONITORING_CHART=packages/system/monitoring

@test "a first install hashes the same password it stores" {
  out=$(mktemp -d)
  for run in 1 2 3; do
    helm template monitoring "$MONITORING_CHART" --namespace tenant-foo \
      --show-only templates/vtraces/central-credentials.yaml \
      --show-only templates/vtraces/collector.yaml \
      --set _namespace.tracingCentral=true --set _namespace.host=example.org \
      > "$out/render-$run.yaml"
    password=$(yq eval 'select(.kind == "Secret") | .stringData.password' "$out/render-$run.yaml")
    checksum=$(yq eval 'select(.kind == "Deployment") | .spec.template.metadata.annotations["checksum/credentials"]' "$out/render-$run.yaml")
    # Separate commands: under set -e a failure on the left of && is not fatal.
    [ -n "$password" ]
    [ "$password" != "null" ]
    [ "$(printf '%s' "$password" | sha256sum | cut -d' ' -f1)" = "$checksum" ]
  done
  rm -rf "$out"
}
