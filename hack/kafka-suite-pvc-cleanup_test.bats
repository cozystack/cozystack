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

@test "kafka backup round-trip deletes each cluster's PVCs after its app cleanup" {
  # examples/backups/kafka creates two apps, kafka-test and kafka-restore; the
  # chart names their Strimzi clusters with a kafka- prefix.
  # One line per finally op, in source order, which is the order they run in.
  # yq emits the facts and awk names the op, because if/then/else only reached
  # yq in v4.54.1 and older ones reject the expression. The test document is
  # cut out first: in a multi-document file yq also evaluates the array literal
  # against the documents the select drops, and prints a line for each.
  sequence=$(yq 'select(.metadata.name == "kafka-2-backup-roundtrip")' "$SUITE" \
    | yq '.spec.steps[].finally[]?
      | [((.script.content // "") | test("examples/backups/kafka/cleanup.sh") | tostring),
         ((.delete.ref.apiVersion // "") + "/" + (.delete.ref.kind // "")),
         (.delete.ref.labels."strimzi.io/cluster" // "")] | join(" ")' \
    | awk '$1 == "true" { print "app-cleanup"; next }
      $2 == "v1/PersistentVolumeClaim" { print "pvc:" $3; next }
      { print "other" }')

  for cluster in kafka-kafka-test kafka-kafka-restore; do
    if ! printf '%s\n' "$sequence" | awk 'seen; /^app-cleanup$/ { seen = 1 }' | grep -qx "pvc:${cluster}"; then
      echo "kafka-2-backup-roundtrip does not delete the PVCs of cluster ${cluster} after cleanup.sh: ${sequence}" >&2
      return 1
    fi
  done
}
