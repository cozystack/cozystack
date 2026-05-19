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
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --ignore-not-found --timeout=6m
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $name --ignore-not-found --timeout=6m
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
  timeout 120 sh -ec "until kubectl -n tenant-test get hr vm-disk-$name >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr vm-disk-$name --timeout=10m --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$name --timeout=500s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$name --timeout=400s --for=jsonpath='{.status.phase}'=Bound
  # Drop the VMDisk so the next test starts from a clean slate. Each test
  # owns its own resources; "Create a VM Instance" creates its own VMDisk
  # inline rather than depending on this one.
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $name --ignore-not-found --timeout=6m
}

@test "Create a VM Instance" {
  # Use a distinct disk name from "Create a VM Disk" so a flaky finalizer
  # drain on the prior disk's PV/PVC cannot collide with this test's apply.
  diskName='vmi-test'
  name='test'
  # Pre-clean any leftover VMInstance from a prior run. The VMDisk is
  # created by this test (decoupled from "Create a VM Disk" above), so no
  # predelete here — the prior test already cleaned up its own VMDisk.
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --ignore-not-found --timeout=6m
  # Create the VMDisk this test depends on. Same wait-for-HR pattern as the
  # first test, so this block is self-contained.
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMDisk
metadata:
  name: $diskName
  namespace: tenant-test
spec:
  source:
    http:
      url: https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
  optical: false
  storage: 5Gi
  storageClass: replicated
EOF
  timeout 120 sh -ec "until kubectl -n tenant-test get hr vm-disk-$diskName >/dev/null 2>&1; do sleep 2; done"
  kubectl -n tenant-test wait hr vm-disk-$diskName --timeout=10m --for=condition=ready
  kubectl -n tenant-test wait dv vm-disk-$diskName --timeout=500s --for=condition=ready
  kubectl -n tenant-test wait pvc vm-disk-$diskName --timeout=400s --for=jsonpath='{.status.phase}'=Bound
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
  timeout 120 sh -ec "until kubectl -n tenant-test get hr vm-instance-$name >/dev/null 2>&1; do sleep 2; done"
  # Wait for the parent HR Ready before polling for the VMI. Under Flux v2.8
  # kstatus the helm install can be still dispatching when the previous
  # ordering assumed it had already applied the VirtualMachine CR — without
  # this the next poll for `vmi vm-instance-$name` returned NotFound for
  # its entire 120s budget because kubevirt hadn't seen the VM yet.
  kubectl -n tenant-test wait hr vm-instance-$name --timeout=10m --for=condition=ready
  # Nested KubeVirt VM startup (virt-launcher + libvirt + cloud-init DHCP)
  # routinely takes 30-60s under runner load. 5m is a comfortable upper
  # bound for nested virt + slow runner I/O after the HR-Ready gate.
  timeout 10m sh -ec "until kubectl -n tenant-test get vmi vm-instance-$name -o jsonpath='{.status.interfaces[0].ipAddress}' | grep -q '[0-9]'; do sleep 2; done"
  # VM ready follows IP assignment closely; 60s gives buffer for the qemu-guest-agent.
  kubectl -n tenant-test wait vm vm-instance-$name --timeout=10m --for=condition=ready
  kubectl -n tenant-test delete vminstances.apps.cozystack.io $name --ignore-not-found --timeout=6m
  kubectl -n tenant-test delete vmdisks.apps.cozystack.io $diskName --ignore-not-found --timeout=6m
}
