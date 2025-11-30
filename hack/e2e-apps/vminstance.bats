#!/usr/bin/env bats

@test "Create a VM Disk" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $name
  namespace: tenant-test
spec:
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  optical: false
  storage: 5Gi
  storageClass: replicated
EOF
  sleep 5
  kubectl -n tenant-test wait hr vm-disk-$name --timeout=5s --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$name --timeout=250s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$name --timeout=200s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait vmdisks vm-disk-$name --timeout=200s --for=condition=ready
}

@test "Create a VM Instance" {
  diskName='test'
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    cores="1000m"
    memory="1Gi"
  else
    cores="2000m"
    memory="2Gi"
  fi
  kubectl -n tenant-test get vminstances.apps.cozystack.io $name || 
  kubectl create -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMInstance
metadata:
  name: $name
  namespace: tenant-test
spec:
  domain:
    cpu:
      cores: "$cores"
  resources:
    requests:
      memory: "$memory"
  external: false
  externalMethod: PortList
  externalPorts:
  - 22
  running: true
  instanceType: "u1.medium"
  instanceProfile: ubuntu
  disks:
    - name: $diskName
  gpus: []
  sshKeys:
  - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF
    test@test
  cloudInit: |
    #cloud-config
    users:
      - name: test
        shell: /bin/bash
        sudo: ['ALL=(ALL) NOPASSWD: ALL']
        groups: sudo
        ssh_authorized_keys:
          - ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPht0dPk5qQ+54g1hSX7A6AUxXJW5T6n/3d7Ga2F8gTF test@test
  cloudInitSeed: ""
EOF
  sleep 5
  kubectl -n tenant-test wait --timeout=5s hr vm-instance-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s vminstances $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=20s vm vm-instance-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=40s vmi vm-instance-$name --for=jsonpath='{status.phase}'=Running
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name 
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $diskName 
}
