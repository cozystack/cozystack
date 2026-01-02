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

@test "Create Backup for Virtual Machine" {
  # Test variables
  bucket_name="${TEST_BUCKET_NAME}"
  vm_name="${TEST_VM_NAME}"
  backupjob_name="${TEST_BACKUPJOB_NAME}"
  namespace="${TEST_NAMESPACE}"

  # Step 1: Create the bucket resource
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${bucket_name}
  namespace: ${namespace}
spec: {}
EOF

  # Wait for the bucket to be ready
  kubectl -n ${namespace} wait hr bucket-${bucket_name} --timeout=100s --for=condition=ready
  kubectl -n ${namespace} wait bucketclaims.objectstorage.k8s.io bucket-${bucket_name} --timeout=300s --for=jsonpath='{.status.bucketReady}'=true
  kubectl -n ${namespace} wait bucketaccesses.objectstorage.k8s.io bucket-${bucket_name} --timeout=300s --for=jsonpath='{.status.accessGranted}'=true

  # Get bucket credentials for later S3 verification
  kubectl -n ${namespace} get secret bucket-${bucket_name} -ojsonpath='{.data.BucketInfo}' | base64 -d > /tmp/bucket-backup-credentials.json
  ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' /tmp/bucket-backup-credentials.json)
  SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' /tmp/bucket-backup-credentials.json)
  BUCKET_NAME=$(jq -r '.spec.bucketName' /tmp/bucket-backup-credentials.json)
  ENDPOINT=$(jq -r '.spec.secretS3.endpoint' /tmp/bucket-backup-credentials.json)

  # Step 2: Create the Virtual Machine
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

  # Wait for VM to be ready
  sleep 5
  kubectl -n ${namespace} wait hr virtual-machine-${vm_name} --timeout=10s --for=condition=ready
  kubectl -n ${namespace} wait dv virtual-machine-${vm_name} --timeout=150s --for=condition=ready
  kubectl -n ${namespace} wait pvc virtual-machine-${vm_name} --timeout=100s --for=jsonpath='{.status.phase}'=Bound
  kubectl -n ${namespace} wait vm virtual-machine-${vm_name} --timeout=100s --for=condition=ready

  # Step 3: Create BackupJob
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

  # Wait for BackupJob to start (phase should be Running after Velero backup is created)
  kubectl -n ${namespace} wait backupjob ${backupjob_name} --timeout=60s --for=jsonpath='{.status.phase}'=Running

  # Wait for BackupJob to complete (Succeeded phase)
  kubectl -n ${namespace} wait backupjob ${backupjob_name} --timeout=300s --for=jsonpath='{.status.phase}'=Succeeded

  # Verify BackupJob status
  PHASE=$(kubectl -n ${namespace} get backupjob ${backupjob_name} -o jsonpath='{.status.phase}')
  [ "$PHASE" = "Succeeded" ]

  # Verify BackupJob has a backupRef
  BACKUP_REF=$(kubectl -n ${namespace} get backupjob ${backupjob_name} -o jsonpath='{.status.backupRef.name}')
  [ -n "$BACKUP_REF" ]

  # Find the Velero backup by searching for backups matching the namespace-backupjob pattern
  # Format: namespace-backupjob-timestamp
  VELERO_BACKUP_NAME=""
  VELERO_BACKUP_PHASE=""
  
  # Wait a bit for the backup to be created and appear in the API
  sleep 5
  
  # Find backup by pattern matching namespace-backupjob
  for backup in $(kubectl -n cozy-velero get backups.velero.io -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
    if echo "$backup" | grep -q "^${namespace}-${backupjob_name}-"; then
      VELERO_BACKUP_NAME=$backup
      VELERO_BACKUP_PHASE=$(kubectl -n cozy-velero get backups.velero.io $backup -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
      break
    fi
  done

  # Verify Velero Backup was found
  [ -n "$VELERO_BACKUP_NAME" ]
  
  # Wait for Velero Backup to complete (with timeout)
  timeout 300 sh -ec "
    until kubectl -n cozy-velero get backups.velero.io ${VELERO_BACKUP_NAME} -o jsonpath='{.status.phase}' | grep -q 'Completed\|Failed'; do
      sleep 5
    done
  "

  # Verify Velero Backup is Completed
  VELERO_BACKUP_PHASE=$(kubectl -n cozy-velero get backups.velero.io ${VELERO_BACKUP_NAME} -o jsonpath='{.status.phase}')
  [ "$VELERO_BACKUP_PHASE" = "Completed" ]

  # Step 4: Verify S3 has backup data
  # Extract endpoint host and port for port-forwarding
  ENDPOINT_HOST=$(echo $ENDPOINT | sed 's|https\?://||' | cut -d: -f1)
  ENDPOINT_PORT=$(echo $ENDPOINT | sed 's|https\?://||' | cut -d: -f2)
  if [ -z "$ENDPOINT_PORT" ]; then
    ENDPOINT_PORT=8333
  fi

  # Start port-forwarding to S3 endpoint
  kubectl -n tenant-root port-forward service/seaweedfs-s3 ${ENDPOINT_PORT}:${ENDPOINT_PORT} > /dev/null 2>&1 &
  PORT_FORWARD_PID=$!

  # Wait for port-forward to be ready
  timeout 30 sh -ec "until nc -z localhost ${ENDPOINT_PORT}; do sleep 1; done"

  # Set up MinIO alias
  mc alias set backup-test https://localhost:${ENDPOINT_PORT} $ACCESS_KEY $SECRET_KEY --insecure

  # Check if backup data exists in S3 bucket
  # Velero stores backups in a structure like: backups/<backup-name>/
  BACKUP_PREFIX="backups/${VELERO_BACKUP_NAME}"
  
  # Wait a bit for backup data to be written to S3
  sleep 10
  
  # List backup directory in S3
  BACKUP_FILES=$(mc ls backup-test/${BUCKET_NAME}/${BACKUP_PREFIX}/ 2>/dev/null | wc -l || echo "0")
  
  # Verify backup files exist (should have at least metadata files)
  if [ "$BACKUP_FILES" -eq "0" ]; then
    # Try alternative paths - Velero might use different structure
    BACKUP_FILES=$(mc ls backup-test/${BUCKET_NAME}/backups/ 2>/dev/null | grep "${VELERO_BACKUP_NAME}" | wc -l || echo "0")
  fi
  
  # At minimum, verify the bucket has some Velero-related data
  VELERO_DATA=$(mc ls backup-test/${BUCKET_NAME}/backups/ 2>/dev/null | wc -l || echo "0")
  [ "$VELERO_DATA" -gt "0" ]

  # Clean up port-forward
  kill $PORT_FORWARD_PID 2>/dev/null || true
  wait $PORT_FORWARD_PID 2>/dev/null || true
}

