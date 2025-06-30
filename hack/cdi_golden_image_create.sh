#!/bin/bash

set -e

name="$1"
url="$2"

if [ -z "$name" ] || [ -z "$url" ]; then
  echo "Usage: <name> <url>"
  echo "Example: 'ubuntu' 'https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img'"
  exit 1
fi

#### create DV ubuntu source for CDI image cloning
kubectl create -f - <<EOF
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: "goldenimage-$name"
  namespace: cozy-public
  annotations:
    cdi.kubevirt.io/storage.bind.immediate.requested: "true"
spec:
  source:
    http:
      url: "$url"
  storage:
    resources:
      requests:
        storage: 5Gi
    storageClassName: replicated
EOF
