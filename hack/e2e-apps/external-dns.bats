#!/usr/bin/env bats

teardown() {
  kubectl -n tenant-test delete externaldns.apps.cozystack.io --all --ignore-not-found 2>/dev/null || true
}

@test "Create and Verify ExternalDNS with inmemory provider" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ExternalDNS
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  provider: inmemory
  domainFilters:
    - example.com
EOF

  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr ${name} >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr ${name} --timeout=120s --for=condition=ready
  kubectl -n tenant-test wait hr ${name}-system --timeout=120s --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get deploy -l app.kubernetes.io/instance=${name}-system -o jsonpath='{.items[0].status.readyReplicas}' | grep -q '1'; do sleep 5; done"

  kubectl -n tenant-test delete externaldns.apps.cozystack.io ${name}
}

@test "Create and Verify ExternalDNS with custom annotationPrefix" {
  name='test-prefix'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: ExternalDNS
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  provider: inmemory
  annotationPrefix: custom-dns/
  domainFilters:
    - example.org
EOF

  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr ${name} >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr ${name} --timeout=120s --for=condition=ready
  kubectl -n tenant-test wait hr ${name}-system --timeout=120s --for=condition=ready
  timeout 60 sh -ec "until kubectl -n tenant-test get deploy -l app.kubernetes.io/instance=${name}-system -o jsonpath='{.items[0].status.readyReplicas}' | grep -q '1'; do sleep 5; done"

  kubectl -n tenant-test delete externaldns.apps.cozystack.io ${name}
}
