#!/usr/bin/env bats

@test "Create VMware VM Import" {
  name='test'

  # Create VMware credentials secret
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: vmware-credentials-${name}
  namespace: tenant-test
type: Opaque
stringData:
  user: administrator@vsphere.local
  password: test-password
  thumbprint: "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD"
EOF

  # Create VMImport resource
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMImport
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  sourceUrl: "https://vcenter.example.com/sdk"
  sourceSecretName: "vmware-credentials-${name}"
  vms:
    - id: "vm-123"
      name: "test-vm-1"
    - id: "vm-456"
      name: "test-vm-2"
  warm: false
  enableAdoption: true
  networkMap:
    - sourceId: "network-1"
      destinationType: "pod"
  storageMap:
    - sourceId: "datastore-1"
      storageClass: "replicated"
EOF

  # Wait for HelmRelease to be ready
  sleep 5
  kubectl -n tenant-test wait hr vm-import-${name} --timeout=100s --for=condition=ready

  # Verify WorkloadMonitor was created
  kubectl -n tenant-test get workloadmonitor vm-import-${name}

  # Verify Forklift Provider resources were created
  timeout 60 sh -ec "until kubectl -n tenant-test get provider.forklift.konveyor.io vm-import-${name}-source 2>/dev/null; do sleep 5; done"
  timeout 60 sh -ec "until kubectl -n tenant-test get provider.forklift.konveyor.io vm-import-${name}-destination 2>/dev/null; do sleep 5; done"

  # Verify Plan was created with correct annotations
  timeout 60 sh -ec "until kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  # Check that Plan has adoption annotations
  adoption_enabled=$(kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} -o jsonpath='{.metadata.annotations.vm-import\.cozystack\.io/adoption-enabled}')
  [ "$adoption_enabled" = "true" ]

  target_namespace=$(kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} -o jsonpath='{.metadata.annotations.vm-import\.cozystack\.io/target-namespace}')
  [ "$target_namespace" = "tenant-test" ]

  import_name=$(kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} -o jsonpath='{.metadata.annotations.vm-import\.cozystack\.io/import-name}')
  [ "$import_name" = "vm-import-${name}" ]

  # Verify NetworkMap was created
  timeout 60 sh -ec "until kubectl -n tenant-test get networkmap.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  # Verify StorageMap was created
  timeout 60 sh -ec "until kubectl -n tenant-test get storagemap.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  # Verify Migration resource was created
  timeout 60 sh -ec "until kubectl -n tenant-test get migration.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  # Verify RBAC permissions for dashboard resources
  kubectl -n tenant-test get role vm-import-${name}-dashboard-resources
  kubectl -n tenant-test get rolebinding vm-import-${name}-dashboard-resources

  # Verify RoleBinding has correct subjects for tenant access
  subjects=$(kubectl -n tenant-test get rolebinding vm-import-${name}-dashboard-resources -o jsonpath='{.subjects[*].name}')
  echo "$subjects" | grep -q "tenant-test"

  # Clean up
  kubectl -n tenant-test delete vmimport.apps.cozystack.io ${name}
  kubectl -n tenant-test delete secret vmware-credentials-${name}

  # Wait for cleanup
  timeout 60 sh -ec "until ! kubectl -n tenant-test get hr vm-import-${name} 2>/dev/null; do sleep 5; done"
}

@test "Create VMImport with minimal configuration" {
  name='test-minimal'

  # Create credentials secret
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: vmware-credentials-${name}
  namespace: tenant-test
type: Opaque
stringData:
  user: admin@vsphere.local
  password: password
  thumbprint: "11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44"
EOF

  # Create VMImport with minimal spec (no networkMap, no storageMap)
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMImport
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  sourceUrl: "https://vcenter-minimal.example.com/sdk"
  sourceSecretName: "vmware-credentials-${name}"
  vms:
    - id: "vm-789"
EOF

  # Wait for HelmRelease to be ready
  sleep 5
  kubectl -n tenant-test wait hr vm-import-${name} --timeout=100s --for=condition=ready

  # Verify basic resources were created
  kubectl -n tenant-test get workloadmonitor vm-import-${name}
  timeout 60 sh -ec "until kubectl -n tenant-test get provider.forklift.konveyor.io vm-import-${name}-source 2>/dev/null; do sleep 5; done"
  timeout 60 sh -ec "until kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  # Verify NetworkMap and StorageMap were NOT created (empty arrays)
  ! kubectl -n tenant-test get networkmap.forklift.konveyor.io vm-import-${name} 2>/dev/null
  ! kubectl -n tenant-test get storagemap.forklift.konveyor.io vm-import-${name} 2>/dev/null

  # Clean up
  kubectl -n tenant-test delete vmimport.apps.cozystack.io ${name}
  kubectl -n tenant-test delete secret vmware-credentials-${name}

  timeout 60 sh -ec "until ! kubectl -n tenant-test get hr vm-import-${name} 2>/dev/null; do sleep 5; done"
}

@test "Create VMImport with adoption disabled" {
  name='test-no-adoption'

  # Create credentials secret
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: vmware-credentials-${name}
  namespace: tenant-test
type: Opaque
stringData:
  user: admin@vsphere.local
  password: password
  thumbprint: "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD"
EOF

  # Create VMImport with adoption disabled
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VMImport
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  sourceUrl: "https://vcenter.example.com/sdk"
  sourceSecretName: "vmware-credentials-${name}"
  enableAdoption: false
  vms:
    - id: "vm-999"
      name: "test-no-adoption-vm"
EOF

  # Wait for HelmRelease to be ready
  sleep 5
  kubectl -n tenant-test wait hr vm-import-${name} --timeout=100s --for=condition=ready

  # Verify Plan has adoption disabled
  timeout 60 sh -ec "until kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} 2>/dev/null; do sleep 5; done"

  adoption_enabled=$(kubectl -n tenant-test get plan.forklift.konveyor.io vm-import-${name} -o jsonpath='{.metadata.annotations.vm-import\.cozystack\.io/adoption-enabled}')
  [ "$adoption_enabled" = "false" ]

  # Clean up
  kubectl -n tenant-test delete vmimport.apps.cozystack.io ${name}
  kubectl -n tenant-test delete secret vmware-credentials-${name}

  timeout 60 sh -ec "until ! kubectl -n tenant-test get hr vm-import-${name} 2>/dev/null; do sleep 5; done"
}
