#!/usr/bin/env bats

@test "Create Etcd" {
  name='test'
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd-$name --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait etcdcluster.etcd.aenix.io etcd --timeout=120s --for=jsonpath='{.status.conditions[?(@.type=="Ready")].status}'=True
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name
}

@test "Create Etcd with backup schedule" {
  name='test-backup'
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Etcd
metadata:
  name: $name
  namespace: tenant-test
spec:
  size: 1Gi
  replicas: 3
  storageClass: ""
  resources:
    cpu: 100m
    memory: 128Mi
  backup:
    enabled: true
    schedule: "0 2 * * *"
    destinationPath: "s3://test-bucket/etcd-backups/"
    endpointURL: "http://minio-gateway-service:9000"
    forcePathStyle: true
    s3AccessKey: "test-access-key"
    s3SecretKey: "test-secret-key"
    successfulJobsHistoryLimit: 3
    failedJobsHistoryLimit: 1
EOF
  sleep 5
  kubectl -n tenant-test wait hr etcd-$name --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait etcdbackupschedule.etcd.aenix.io etcd --timeout=60s --for=jsonpath='{.spec.schedule}'='0 2 * * *'
  kubectl -n tenant-test get secret etcd-s3-creds
  kubectl -n tenant-test delete etcd.apps.cozystack.io $name
}
