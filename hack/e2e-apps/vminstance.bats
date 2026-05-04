#!/usr/bin/env bats

@test "Create a VM Disk" {
  name='test'
  # Delete any leftover from a previous run and BLOCK until removal completes.
  # `kubectl apply` on a resource still in finalizer-drain silently no-ops
  # ("Detected changes to resource ... which is currently being deleted"),
  # which then races a NotFound on the downstream HR wait. The previous
  # `|| true` swallowed timeout errors here and let the test continue with
  # the old VMDisk still draining. Bumped timeout to 3m and removed `|| true`
  # so a true delete failure surfaces immediately.
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --ignore-not-found --timeout=3m
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $name --ignore-not-found --timeout=3m
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
  # Wait for the operator to materialise the HelmRelease before kubectl wait
  # kicks in (kubectl wait errors immediately if the object does not exist yet).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr vm-disk-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr vm-disk-$name --timeout=5s --for=condition=ready
  timeout 120 sh -ec "until kubectl -n tenant-test get dv vm-disk-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait dv vm-disk-$name --timeout=250s --for=condition=ready
  timeout 120 sh -ec "until kubectl -n tenant-test get pvc vm-disk-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait pvc vm-disk-$name --timeout=200s --for=jsonpath='{.status.phase}'=Bound
}

@test "Create a VM Instance" {
  diskName='test'
  name='test'
  # Same delete-finalizer-drain race as in "Create a VM Disk" above —
  # block until removal completes, surface timeouts loudly.
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --ignore-not-found --timeout=3m
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMInstance
metadata:
  name: $name
  namespace: tenant-test
spec:
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
  # Wait for the operator to materialise the HelmRelease before downstream
  # waits proceed (kubectl wait errors immediately if the HR does not exist).
  timeout 60 sh -ec "until kubectl -n tenant-test get hr vm-instance-$name >/dev/null 2>&1; do sleep 2; done"
  # Nested KubeVirt VM startup (virt-launcher + libvirt + cloud-init DHCP)
  # routinely takes 30-60s under runner load; the previous 20s was unrealistic
  # and produced flakes. 120s is a comfortable upper bound for nested virt.
  timeout 120 sh -ec "until kubectl -n tenant-test get vmi vm-instance-$name -o jsonpath='{.status.interfaces[0].ipAddress}' | grep -q '[0-9]'; do sleep 2; done"
  kubectl -n tenant-test wait hr vm-instance-$name --timeout=5s --for=condition=ready
  # VM ready follows IP assignment closely; 60s gives buffer for the qemu-guest-agent.
  timeout 120 sh -ec "until kubectl -n tenant-test get vm vm-instance-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait vm vm-instance-$name --timeout=60s --for=condition=ready
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --timeout=3m
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $diskName --timeout=3m
}
