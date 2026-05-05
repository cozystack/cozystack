#!/usr/bin/env bats

@test "Create Kafka" {
  name='test'
  kubectl -n tenant-test delete kafka.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl -n tenant-test wait kafka.apps.cozystack.io $name --for=delete --timeout=2m 2>/dev/null || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Kafka
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  kafka:
    size: 10Gi
    replicas: 2
    storageClass: ""
    resources: {}
    resourcesPreset: "nano"
  zookeeper:
    size: 5Gi
    replicas: 2
    storageClass: ""
    resources:
    resourcesPreset: "nano"
  topics:
    - name: testResults
      partitions: 1
      replicas: 2
      config:
        min.insync.replicas: 2
    - name: testOrders
      config:
        cleanup.policy: compact
        segment.ms: 3600000
        max.compaction.lag.ms: 5400000
        min.insync.replicas: 2
      partitions: 1
      replicas: 2
EOF
  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr kafka-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr kafka-$name --timeout=30s --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get kafkas test >/dev/null 2>&1; do sleep 2; done"
  kubectl wait kafkas -n tenant-test test --timeout=300s --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get pvc data-kafka-$name-zookeeper-0; do sleep 10; done"
  kubectl -n tenant-test wait pvc data-kafka-$name-zookeeper-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  timeout 40 sh -ec "until kubectl -n tenant-test get svc kafka-$name-zookeeper-client -o jsonpath='{.spec.ports[0].port}' | grep -q '2181'; do sleep 10; done"
  timeout 40 sh -ec "until kubectl -n tenant-test get svc kafka-$name-zookeeper-nodes -o jsonpath='{.spec.ports[*].port}' | grep -q '2181 2888 3888'; do sleep 10; done"
  timeout 80 sh -ec "until kubectl -n tenant-test get endpoints kafka-$name-zookeeper-nodes -o jsonpath='{.subsets[*].addresses[0].ip}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test delete kafka.apps.cozystack.io $name
  kubectl -n tenant-test delete pvc data-kafka-$name-zookeeper-0
  kubectl -n tenant-test delete pvc data-kafka-$name-zookeeper-1
}
