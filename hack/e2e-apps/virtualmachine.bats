#!/usr/bin/env bats

@test "Create a Virtual Machine" {
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VirtualMachine
metadata:
  name: $name
  namespace: tenant-test
spec:
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
  sleep 5
  kubectl -n tenant-test wait hr virtual-machine-$name --timeout=10s --for=condition=ready
  kubectl -n tenant-test wait dv virtual-machine-$name --timeout=150s --for=condition=ready
  kubectl -n tenant-test wait pvc virtual-machine-$name --timeout=100s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n tenant-test wait vm virtual-machine-$name --timeout=100s --for=condition=ready
  timeout 120 sh -ec "until kubectl -n tenant-test get vmi virtual-machine-$name -o jsonpath='{.status.interfaces[0].ipAddress}' | grep -q '[0-9]'; do sleep 10; done"
  kubectl -n tenant-test delete virtualmachines.apps.cozystack.io $name 
}
