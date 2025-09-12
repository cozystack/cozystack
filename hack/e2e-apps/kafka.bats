#!/usr/bin/env bats

@test "Create Kafka" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
  resources:
    cpu: 500m
    memory: 768Mi
EOF
  )
  else
    resources='resources: {}'
  fi
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
    $resources
    resourcesPreset: "nano"
  zookeeper:
    size: 5Gi
    replicas: 2
    storageClass: ""
    $resources
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
  sleep 5
  kubectl -n tenant-test wait --timeout=30s hr kafka-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=1m kafkas $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=50s pvc data-kafka-$name-zookeeper-0 --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait --timeout=40s svc kafka-$name-zookeeper-client --for=jsonpath='{.spec.ports[0].port}'=2181
  kubectl -n tenant-test delete kafka.apps.cozystack.io $name
}
