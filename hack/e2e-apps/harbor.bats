#!/usr/bin/env bats

@test "Create Harbor" {
  name='harbor'
  kubectl apply -f- <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Harbor
metadata:
  name: $name
  namespace: tenant-test
spec:
  host: ""
  storageClass: ""
  core:
    resources: {}
    resourcesPreset: "nano"
  registry:
    size: 5Gi
    resources: {}
    resourcesPreset: "nano"
  jobservice:
    resources: {}
    resourcesPreset: "nano"
  trivy:
    enabled: false
    size: 2Gi
    resources: {}
    resourcesPreset: "nano"
  database:
    size: 2Gi
    replicas: 1
  redis:
    size: 1Gi
    replicas: 1
EOF
  sleep 5
  kubectl -n tenant-test wait hr $name --timeout=60s --for=condition=ready
  kubectl -n tenant-test wait hr $name-system --timeout=300s --for=condition=ready
  kubectl -n tenant-test wait deploy $name-core --timeout=120s --for=condition=available
  kubectl -n tenant-test wait deploy $name-registry --timeout=120s --for=condition=available
  kubectl -n tenant-test wait deploy $name-portal --timeout=120s --for=condition=available
  kubectl -n tenant-test get secret $name-credentials -o jsonpath='{.data.admin-password}' | base64 --decode | grep -q '.'
  kubectl -n tenant-test get secret $name-credentials -o jsonpath='{.data.url}' | base64 --decode | grep -q 'https://'
  kubectl -n tenant-test get svc $name -o jsonpath='{.spec.ports[0].port}' | grep -q '80'
  kubectl -n tenant-test delete harbor.apps.cozystack.io $name
}
