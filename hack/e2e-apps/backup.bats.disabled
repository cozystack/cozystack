#!/usr/bin/env bats

# Test variables - stored for teardown
TEST_NAMESPACE='tenant-test'
TEST_BUCKET_NAME='test-backup-bucket'
TEST_VM_NAME='test-backup-vm'
TEST_BACKUPJOB_NAME='test-backup-job'

teardown() {
  # Clean up resources (runs even if test fails)
  namespace="${TEST_NAMESPACE}"
  bucket_name="${TEST_BUCKET_NAME}"
  vm_name="${TEST_VM_NAME}"
  backupjob_name="${TEST_BACKUPJOB_NAME}"
  
  # Clean up port-forward if still running
  pkill -f "kubectl.*port-forward.*seaweedfs-s3" 2>/dev/null || true
  
  # Clean up Velero resources in cozy-velero namespace
  # Find Velero backup by pattern matching namespace-backupjob
  for backup in $(kubectl -n cozy-velero get backups.velero.io -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true); do
    if echo "$backup" | grep -q "^${namespace}-${backupjob_name}-"; then
      kubectl -n cozy-velero delete backups.velero.io ${backup} --wait=false 2>/dev/null || true
    fi
  done
  
  # Clean up BackupStorageLocation and VolumeSnapshotLocation (named: namespace-backupjob)
  BSL_NAME="${namespace}-${backupjob_name}"
  kubectl -n cozy-velero delete backupstoragelocations.velero.io ${BSL_NAME} --wait=false 2>/dev/null || true
  kubectl -n cozy-velero delete volumesnapshotlocations.velero.io ${BSL_NAME} --wait=false 2>/dev/null || true
  
  # Clean up Velero credentials secret
  SECRET_NAME="backup-${namespace}-${backupjob_name}-s3-credentials"
  kubectl -n cozy-velero delete secret ${SECRET_NAME} --wait=false 2>/dev/null || true
  
  # Clean up BackupJob
  kubectl -n ${namespace} delete backupjob ${backupjob_name} --wait=false 2>/dev/null || true
  
  # Clean up Virtual Machine
  kubectl -n ${namespace} delete virtualmachines.apps.cozystack.io ${vm_name} --wait=false 2>/dev/null || true
  
  # Clean up Bucket
  kubectl -n ${namespace} delete bucket.apps.cozystack.io ${bucket_name} --wait=false 2>/dev/null || true

  # Clean up temporary files
  rm -f /tmp/bucket-backup-credentials.json
}

print_log() {
  echo "# $1" >&3
}

@test "Create Backup for Virtual Machine" {
  # Test variables
  bucket_name="${TEST_BUCKET_NAME}"
  vm_name="${TEST_VM_NAME}"
  backupjob_name="${TEST_BACKUPJOB_NAME}"
  namespace="${TEST_NAMESPACE}"

  print_log "Step 0:Ensure BackupJob and Velero strategy CRDs are installed"
  kubectl apply -f packages/system/backup-controller/definitions/backups.cozystack.io_backupjobs.yaml
  kubectl apply -f packages/system/backupstrategy-controller/definitions/strategy.backups.cozystack.io_veleroes.yaml
  # Wait for CRDs to be ready
  kubectl wait --for condition=established --timeout=30s crd backupjobs.backups.cozystack.io
  kubectl wait --for condition=established --timeout=30s crd veleroes.strategy.backups.cozystack.io
  
  # Ensure velero-strategy-default resource exists
  kubectl apply -f packages/system/backup-controller/templates/strategy.yaml

  print_log "Step 1: Create the bucket resource"
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${bucket_name}
  namespace: ${namespace}
spec: {}
EOF

  print_log "Wait for the bucket to be ready"
  kubectl -n ${namespace} wait hr bucket-${bucket_name} --timeout=100s --for=condition=ready
  kubectl -n ${namespace} wait bucketclaims.objectstorage.k8s.io bucket-${bucket_name} --timeout=300s --for=jsonpath='{.status.bucketReady}'=true
  kubectl -n ${namespace} wait bucketaccesses.objectstorage.k8s.io bucket-${bucket_name} --timeout=300s --for=jsonpath='{.status.accessGranted}'=true

  # Get bucket credentials for later S3 verification
  kubectl -n ${namespace} get secret bucket-${bucket_name} -ojsonpath='{.data.BucketInfo}' | base64 -d > /tmp/bucket-backup-credentials.json
  ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' /tmp/bucket-backup-credentials.json)
  SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' /tmp/bucket-backup-credentials.json)
  BUCKET_NAME=$(jq -r '.spec.bucketName' /tmp/bucket-backup-credentials.json)

  print_log "Step 2: Create the Virtual Machine"
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: VirtualMachine
metadata:
  name: ${vm_name}
  namespace: ${namespace}
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

  print_log "Wait for VM to be ready"
  sleep 5
  kubectl -n ${namespace} wait hr virtual-machine-${vm_name} --timeout=10s --for=condition=ready
  kubectl -n ${namespace} wait dv virtual-machine-${vm_name} --timeout=150s --for=condition=ready
  kubectl -n ${namespace} wait pvc virtual-machine-${vm_name} --timeout=100s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n ${namespace} wait vm virtual-machine-${vm_name} --timeout=100s --for=condition=ready

  print_log "Step 3: Create BackupJob"
  kubectl apply -f - <<EOF
apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: ${backupjob_name}
  namespace: ${namespace}
  labels:
    backups.cozystack.io/triggered-by: e2e-test
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: VirtualMachine
    name: ${vm_name}
  storageRef:
    apiGroup: apps.cozystack.io
    kind: Bucket
    name: ${bucket_name}
  strategyRef:
    apiGroup: strategy.backups.cozystack.io
    kind: Velero
    name: velero-strategy-default
EOF

  print_log "Wait for BackupJob to start"
  kubectl -n ${namespace} wait backupjob ${backupjob_name} --timeout=60s --for=jsonpath='{.status.phase}'=Running

  print_log "Wait for BackupJob to complete"
  kubectl -n ${namespace} wait backupjob ${backupjob_name} --timeout=300s --for=jsonpath='{.status.phase}'=Succeeded

  print_log "Verify BackupJob status"
  PHASE=$(kubectl -n ${namespace} get backupjob ${backupjob_name} -o jsonpath='{.status.phase}')
  [ "$PHASE" = "Succeeded" ]

  # Verify BackupJob has a backupRef
  BACKUP_REF=$(kubectl -n ${namespace} get backupjob ${backupjob_name} -o jsonpath='{.status.backupRef.name}')
  [ -n "$BACKUP_REF" ]

  # Find the Velero backup by searching for backups matching the namespace-backupjob pattern
  # Format: namespace-backupjob-timestamp
  VELERO_BACKUP_NAME=""
  VELERO_BACKUP_PHASE=""
  
  print_log "Wait a bit for the backup to be created and appear in the API"
  sleep 30
  
  # Find backup by pattern matching namespace-backupjob
  for backup in $(kubectl -n cozy-velero get backups.velero.io -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
    if echo "$backup" | grep -q "^${namespace}-${backupjob_name}-"; then
      VELERO_BACKUP_NAME=$backup
      VELERO_BACKUP_PHASE=$(kubectl -n cozy-velero get backups.velero.io $backup -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
      break
    fi
  done

  print_log "Verify Velero Backup was found"
  [ -n "$VELERO_BACKUP_NAME" ]
  
  echo '# Wait for Velero Backup to complete' >&3
  until kubectl -n cozy-velero get backups.velero.io ${VELERO_BACKUP_NAME} -o jsonpath='{.status.phase}' | grep -q 'Completed\|Failed'; do
    sleep 5
  done

  print_log "Verify Velero Backup is Completed"
  timeout 90 sh -ec "until [ \"\$(kubectl -n cozy-velero get backups.velero.io ${VELERO_BACKUP_NAME} -o jsonpath='{.status.phase}' 2>/dev/null)\" = \"Completed\" ]; do sleep 30; done"
  
  # Final verification
  VELERO_BACKUP_PHASE=$(kubectl -n cozy-velero get backups.velero.io ${VELERO_BACKUP_NAME} -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
  [ "$VELERO_BACKUP_PHASE" = "Completed" ]

  print_log "Step 4: Verify S3 has backup data"
  # Start port-forwarding to S3 service (with timeout to keep it alive)
  bash -c 'timeout 100s kubectl port-forward service/seaweedfs-s3 -n tenant-root 8333:8333 > /dev/null 2>&1 &'

  # Wait for port-forward to be ready
  timeout 30 sh -ec "until nc -z localhost 8333; do sleep 1; done"

  # Wait a bit for backup data to be written to S3
  sleep 30
  
  # Set up MinIO client with insecure flag (use environment variable for all commands)
  export MC_INSECURE=1
  mc alias set local https://localhost:8333 $ACCESS_KEY $SECRET_KEY

  # Verify backup directory exists in S3
  BACKUP_PATH="${BUCKET_NAME}/backups/${VELERO_BACKUP_NAME}"
  mc ls local/${BACKUP_PATH}/ 2>/dev/null
  [ $? -eq 0 ]
  
  # Verify backup files exist (at least metadata files)
  BACKUP_FILES=$(mc ls local/${BACKUP_PATH}/ 2>/dev/null | wc -l || echo "0")
  [ "$BACKUP_FILES" -gt "0" ]
}

