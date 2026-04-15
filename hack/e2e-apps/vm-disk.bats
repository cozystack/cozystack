#!/usr/bin/env bats

# Tests for vm-disk source types: source.image and source.disk.
# Existing source.http coverage lives in vminstance.bats.

@test "Create a VM Disk from source.image (golden image clone)" {
  name='test-image-src'
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $name --ignore-not-found --timeout=2m || true
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $name
  namespace: tenant-test
spec:
  source:
    image:
      name: alpine-3.21
  optical: false
  storage: 5Gi
  storageClass: replicated
EOF
  sleep 5
  kubectl -n tenant-test wait hr vm-disk-$name --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$name --timeout=250s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$name --timeout=200s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $name
}

@test "Create a VM Disk from source.disk (PVC clone)" {
  base='test-disk-base'
  clone='test-disk-clone'

  # Ensure both resources are absent before starting
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $clone --ignore-not-found --timeout=2m || true
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $base --ignore-not-found --timeout=2m || true

  # Create the base disk via source.http. Alpine is ~50MB vs Ubuntu noble's ~600MB,
  # which keeps this test fast; the assertion here is the clone step, not the HTTP import.
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $base
  namespace: tenant-test
spec:
  source:
    http:
      url: https://dl-cdn.alpinelinux.org/alpine/v3.21/releases/cloud/nocloud_alpine-3.21.6-x86_64-bios-cloudinit-r0.qcow2
  optical: false
  storage: 5Gi
  storageClass: replicated
EOF
  sleep 5
  kubectl -n tenant-test wait hr vm-disk-$base --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$base --timeout=250s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$base --timeout=200s --for=jsonpath='{.status.phase}'=Bound

  # Now clone the base disk using source.disk
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $clone
  namespace: tenant-test
spec:
  source:
    disk:
      name: $base
  optical: false
  storage: 5Gi
  storageClass: replicated
EOF
  sleep 5
  kubectl -n tenant-test wait hr vm-disk-$clone --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$clone --timeout=250s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$clone --timeout=200s --for=jsonpath='{.status.phase}'=Bound

  # Cleanup both
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $clone
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $base
}
