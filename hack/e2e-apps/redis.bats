#!/usr/bin/env bats

@test "Create Redis" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    resources=$(cat <<EOF
resources:
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
  kubectl -n tenant-test wait --timeout=20s hr redis-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s redis.apps.cozystack.io $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=50s pvc redisfailover-persistent-data-rfr-redis-$name-0 --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait --timeout=90s sts rfr-redis-$name --for=jsonpath='{.status.replicas}'=2
  sleep 45
  kubectl -n tenant-test wait --timeout=45s deploy rfs-redis-$name --for=condition=available
  kubectl -n tenant-test delete redis.apps.cozystack.io $name
}
