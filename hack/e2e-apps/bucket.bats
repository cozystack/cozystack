#!/usr/bin/env bats

@test "Create and Verify Seeweedfs Bucket" {
  # Create the bucket resource with readwrite and readonly users
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${name}
  namespace: tenant-test
spec:
  users:
    admin: {}
    viewer:
      readonly: true
EOF

  # Wait for the bucket to be ready
  kubectl -n tenant-test wait hr bucket-${name} --timeout=100s --for=condition=ready
  kubectl -n tenant-test wait bucketclaims.objectstorage.k8s.io bucket-${name} --timeout=300s --for=jsonpath='{.status.bucketReady}'
  kubectl -n tenant-test wait bucketaccesses.objectstorage.k8s.io bucket-${name}-admin --timeout=300s --for=jsonpath='{.status.accessGranted}'
  kubectl -n tenant-test wait bucketaccesses.objectstorage.k8s.io bucket-${name}-viewer --timeout=300s --for=jsonpath='{.status.accessGranted}'

  # Get admin (readwrite) credentials
  kubectl -n tenant-test get secret bucket-${name}-admin -ojsonpath='{.data.BucketInfo}' | base64 -d > bucket-admin-credentials.json
  ADMIN_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' bucket-admin-credentials.json)
  ADMIN_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' bucket-admin-credentials.json)
  BUCKET_NAME=$(jq -r '.spec.bucketName' bucket-admin-credentials.json)

  # Get viewer (readonly) credentials
  kubectl -n tenant-test get secret bucket-${name}-viewer -ojsonpath='{.data.BucketInfo}' | base64 -d > bucket-viewer-credentials.json
  VIEWER_ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' bucket-viewer-credentials.json)
  VIEWER_SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' bucket-viewer-credentials.json)

  # Start port-forwarding
  bash -c 'timeout 100s kubectl port-forward service/seaweedfs-s3 -n tenant-root 8333:8333 > /dev/null 2>&1 &'

  # Wait for port-forward to be ready
  timeout 30 sh -ec 'until nc -z localhost 8333; do sleep 1; done'

  # --- Test readwrite user (admin) ---
  mc alias set rw-user https://localhost:8333 $ADMIN_ACCESS_KEY $ADMIN_SECRET_KEY --insecure

  # Admin can upload
  echo "readwrite test" > /tmp/rw-test.txt
  mc cp --insecure /tmp/rw-test.txt rw-user/$BUCKET_NAME/rw-test.txt

  # Admin can list
  mc ls --insecure rw-user/$BUCKET_NAME/rw-test.txt

  # Admin can download
  mc cp --insecure rw-user/$BUCKET_NAME/rw-test.txt /tmp/rw-test-download.txt

  # --- Test readonly user (viewer) ---
  mc alias set ro-user https://localhost:8333 $VIEWER_ACCESS_KEY $VIEWER_SECRET_KEY --insecure

  # Viewer can list
  mc ls --insecure ro-user/$BUCKET_NAME/rw-test.txt

  # Viewer can download
  mc cp --insecure ro-user/$BUCKET_NAME/rw-test.txt /tmp/ro-test-download.txt

  # Viewer cannot upload (must fail)
  echo "readonly test" > /tmp/ro-test.txt
  run mc cp --insecure /tmp/ro-test.txt ro-user/$BUCKET_NAME/ro-test.txt
  [ "$status" -ne 0 ]

  # --- Cleanup ---
  mc rm --insecure rw-user/$BUCKET_NAME/rw-test.txt
  kubectl -n tenant-test delete bucket.apps.cozystack.io ${name}
}
