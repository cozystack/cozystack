#!/usr/bin/env bats

@test "Create Harbor" {
  name='test'
  release="harbor-$name"
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
  kubectl -n tenant-test wait hr $release --timeout=60s --for=condition=ready

  # Wait for COSI to provision bucket
  kubectl -n tenant-test wait bucketclaims.objectstorage.k8s.io $release-registry \
    --timeout=300s --for=jsonpath='{.status.bucketReady}'=true
  kubectl -n tenant-test wait bucketaccesses.objectstorage.k8s.io $release-registry \
    --timeout=60s --for=jsonpath='{.status.accessGranted}'=true

  kubectl -n tenant-test wait hr $release-system --timeout=600s --for=condition=ready || {
    echo "=== HelmRelease status ==="
    kubectl -n tenant-test get hr $release-system -o yaml 2>&1 || true
    echo "=== Pods ==="
    kubectl -n tenant-test get pods 2>&1 || true
    echo "=== Events ==="
    kubectl -n tenant-test get events --sort-by='.lastTimestamp' 2>&1 | tail -30 || true
    echo "=== ExternalArtifact ==="
    kubectl -n cozy-system get externalartifact cozystack-harbor-application-default-harbor-system -o yaml 2>&1 || true
    echo "=== BucketClaim status ==="
    kubectl -n tenant-test get bucketclaims.objectstorage.k8s.io $release-registry -o yaml 2>&1 || true
    echo "=== BucketAccess status ==="
    kubectl -n tenant-test get bucketaccesses.objectstorage.k8s.io $release-registry -o yaml 2>&1 || true
    echo "=== BucketAccess Secret ==="
    kubectl -n tenant-test get secret $release-registry-bucket -o jsonpath='{.data.BucketInfo}' 2>&1 | base64 -d 2>&1 || true
    false
  }
  kubectl -n tenant-test wait deploy $release-core --timeout=120s --for=condition=available
  kubectl -n tenant-test wait deploy $release-registry --timeout=120s --for=condition=available
  kubectl -n tenant-test wait deploy $release-portal --timeout=120s --for=condition=available
  kubectl -n tenant-test get secret $release-credentials -o jsonpath='{.data.admin-password}' | base64 --decode | grep -q '.'
  kubectl -n tenant-test get secret $release-credentials -o jsonpath='{.data.url}' | base64 --decode | grep -q 'https://'
  kubectl -n tenant-test get svc $release -o jsonpath='{.spec.ports[0].port}' | grep -q '80'
  kubectl -n tenant-test delete harbor.apps.cozystack.io $name
}
