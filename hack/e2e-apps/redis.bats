#!/usr/bin/env bats

@test "Create Redis" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
resources:
  requests:
    cpu: 500m
    memory: 768Mi
  limits:
    cpu: "1000m"
    memory: "1Gi"
EOF
  )
  else
    resources='resources: {}'
  fi
  kubectl -n tenant-test get redis.apps.cozystack.io $name || 
  kubectl create -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Redis
metadata:
  name: $name
  namespace: tenant-test
spec:
  external: false
  size: 1Gi
  replicas: 2
  storageClass: ""
  authEnabled: true
  $resources
  resourcesPreset: "nano"
EOF
  sleep 5
  kubectl -n tenant-test wait hr redis-$name --timeout=20s --for=condition=ready
  kubectl -n tenant-test wait pvc redisfailover-persistent-data-rfr-redis-$name-0 --timeout=50s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait deploy rfs-redis-$name --timeout=90s --for=condition=available
  kubectl -n tenant-test wait sts rfr-redis-$name --timeout=90s --for=jsonpath='{.status.replicas}'=2
  kubectl -n tenant-test delete redis.apps.cozystack.io $name
}