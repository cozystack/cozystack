#!/bin/sh

set -e

usage() {
    echo "Usage: $0 <lock|unlock> <namespace> <bucket-name>"
    echo ""
    echo "Commands:"
    echo "  lock    - Block deletion and modification of objects in the bucket"
    echo "  unlock  - Restore full access to the bucket"
    echo ""
    echo "Example:"
    echo "  $0 lock tenant-root somebucket"
    echo "  $0 unlock tenant-root somebucket"
    exit 1
}

if [ $# -ne 3 ]; then
    usage
fi

ACTION="$1"
NAMESPACE="$2"
BUCKET_NAME="$3"

if [ "$ACTION" != "lock" ] && [ "$ACTION" != "unlock" ]; then
    echo "Error: First argument must be 'lock' or 'unlock'"
    usage
fi

# Check if bucket exists
if ! kubectl get buckets.apps.cozystack.io -n "$NAMESPACE" "$BUCKET_NAME" > /dev/null 2>&1; then
    echo "Error: Bucket '$BUCKET_NAME' not found in namespace '$NAMESPACE'"
    exit 1
fi

# Get secret and extract bucket config and bucket name using go-template + jq
SECRET_NAME="bucket-$BUCKET_NAME"
BUCKET_INFO=$(kubectl get secret -n "$NAMESPACE" "$SECRET_NAME" -o go-template='{{ .data.BucketInfo | base64decode }}')
BUCKET_CONFIG=$(echo "$BUCKET_INFO" | jq -r '.metadata.name')
S3_BUCKET_NAME=$(echo "$BUCKET_INFO" | jq -r '.spec.bucketName')

# Convert bc- prefix to ba- for bucket account username
BUCKET_ACCOUNT=$(echo "$BUCKET_CONFIG" | sed 's/^bc-/ba-/')

if [ -z "$BUCKET_ACCOUNT" ] || [ -z "$S3_BUCKET_NAME" ]; then
    echo "Error: Could not extract bucket account or bucket name from secret '$SECRET_NAME'"
    exit 1
fi

# Get seaweedfs namespace from namespace annotation
SEAWEEDFS_NS=$(kubectl get namespace "$NAMESPACE" -o jsonpath='{.metadata.annotations.namespace\.cozystack\.io/seaweedfs}')

if [ -z "$SEAWEEDFS_NS" ]; then
    echo "Error: Could not find seaweedfs namespace annotation on namespace '$NAMESPACE'"
    exit 1
fi

# Build the s3.configure command
ACTIONS="Read:$S3_BUCKET_NAME,Write:$S3_BUCKET_NAME,List:$S3_BUCKET_NAME,Tagging:$S3_BUCKET_NAME"

if [ "$ACTION" = "lock" ]; then
    S3_CMD="s3.configure -actions $ACTIONS -user $BUCKET_ACCOUNT --delete --apply"
    echo "Locking bucket '$BUCKET_NAME' in namespace '$NAMESPACE'..."
else
    S3_CMD="s3.configure -actions $ACTIONS -user $BUCKET_ACCOUNT --apply"
    echo "Unlocking bucket '$BUCKET_NAME' in namespace '$NAMESPACE'..."
fi

echo "Executing command in seaweedfs-master-0 (namespace: $SEAWEEDFS_NS):"
echo "  $S3_CMD"
echo ""

# Execute the command
echo "$S3_CMD" | kubectl exec -i -n "$SEAWEEDFS_NS" seaweedfs-master-0 -- weed shell

echo ""
echo "Done. Bucket '$BUCKET_NAME' has been ${ACTION}ed."
