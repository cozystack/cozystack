#!/usr/bin/env bats
# The kafka chart sets deleteClaim: false on both node pools, so deleting a
# Kafka app leaves its broker and controller PVCs behind on purpose. The e2e
# suite has to delete them itself, after the app is gone (a mounted PVC cannot
# finish deleting), or every later suite in the sandbox runs short of LINSTOR
# space. These tests read hack/e2e-chainsaw/kafka/chainsaw-test.yaml, because
# running it needs a cluster.

load test_helper

SUITE=hack/e2e-chainsaw/kafka/chainsaw-test.yaml

@test "kafka test deletes its PVCs in the cleanup of the step that applies the app" {
  # Chainsaw runs a step's cleanup ops after that step's own cleaner has
  # deleted what the step applied, on a passing and a failing run alike.
  clusters=$(yq 'select(.metadata.name == "kafka") | .spec.steps[]
    | select([.try[]? | select(.apply.file == "kafka.yaml")] | length > 0)
    | .cleanup[]?.delete.ref | select(.apiVersion == "v1" and .kind == "PersistentVolumeClaim")
    | .labels."strimzi.io/cluster"' "$SUITE")

  if ! printf '%s\n' "$clusters" | grep -qx kafka-test; then
    echo "kafka test does not delete the PVCs of cluster kafka-test after deleting the app" >&2
    return 1
  fi
}
