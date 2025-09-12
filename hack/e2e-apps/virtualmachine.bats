#!/usr/bin/env bats

@test "Create a Virtual Machine" {
  name='test'
  withResources='true'
  if [ "$withResources" == 'true' ]; then
    cores="1000m"
    memory="1Gi
  else
    cores="2000m"
    memory="2Gi
  fi
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VirtualMachine
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
  instanceType: "u1.medium"
  instanceProfile: ubuntu
  systemDisk:
    image: ubuntu
    storage: 5Gi
    storageClass: replicated
  gpus: []
  resources: {}
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
  sleep 10
  kubectl -n tenant-test wait --timeout=10s hr virtual-machine-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s virtualmachines $name --for=condition=ready
  kubectl -n tenant-test wait --timeout=130s pvc virtual-machine-$name --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait --timeout=150s dv virtual-machine-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=100s vm virtual-machine-$name --for=condition=ready
  kubectl -n tenant-test wait --timeout=150s vmi virtual-machine-$name --for=jsonpath='{status.phase}'=Running
  kubectl -n tenant-test delete virtualmachines.apps.cozystack.io $name 
}
