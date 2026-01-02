#!/usr/bin/env bats

@test "Create and Verify Seeweedfs Bucket" {
  # Create the bucket resource
  name='test'
  kubectl apply -f - <<EOF
apiVersion: apps.cozystack.io/v1alpha1
kind: Bucket
metadata:
  name: ${name}
  namespace: tenant-test
spec: {}
EOF

  # Wait for the bucket to be ready
  kubectl -n tenant-test wait hr bucket-${name} --timeout=100s --for=condition=ready
  kubectl -n tenant-test wait bucketclaims.objectstorage.k8s.io bucket-${name} --timeout=300s --for=jsonpath='{.status.bucketReady}'
  kubectl -n tenant-test wait bucketaccesses.objectstorage.k8s.io bucket-${name} --timeout=300s --for=jsonpath='{.status.accessGranted}'

  # Get and decode credentials
  kubectl -n tenant-test get secret bucket-${name} -ojsonpath='{.data.BucketInfo}' | base64 -d > bucket-test-credentials.json

  # Get credentials from the secret
  ACCESS_KEY=$(jq -r '.spec.secretS3.accessKeyID' bucket-test-credentials.json)
  SECRET_KEY=$(jq -r '.spec.secretS3.accessSecretKey' bucket-test-credentials.json)
  BUCKET_NAME=$(jq -r '.spec.bucketName' bucket-test-credentials.json)

  # Tmp for test via s3 endpoint from bucket credentials
  S3_ENDPOINT=$(jq -r '.spec.secretS3.endpoint' bucket-test-credentials.json)
  echo "# S3 endopint = $S3_ENDPOINT" >&3

  # Start port-forwarding
  bash -c 'timeout 100s kubectl port-forward service/seaweedfs-s3 -n tenant-root 8333:8333 > /dev/null 2>&1 &'

  # Wait for port-forward to be ready
  timeout 30 sh -ec 'until nc -z localhost 8333; do sleep 1; done'

  # Set up MinIO alias with error handling
  mc alias set local $S3_ENDPOINT $ACCESS_KEY $SECRET_KEY --insecure

  # Upload file to bucket
  mc cp bucket-test-credentials.json $BUCKET_NAME/bucket-test-credentials.json

  # Verify file was uploaded
  mc ls $BUCKET_NAME/bucket-test-credentials.json

  # Clean up uploaded file
  mc rm $BUCKET_NAME/bucket-test-credentials.json

  kubectl -n tenant-test delete bucket.apps.cozystack.io ${name}
}
