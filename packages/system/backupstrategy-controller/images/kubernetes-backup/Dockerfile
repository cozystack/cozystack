# kubernetes-backup runs the Kubernetes strategy Pod: it dumps a tenant
# cluster's key prefix from its etcd DataStore with etcdctl, moves the dump to
# S3 with curl --aws-sigv4, and writes it back with etcdctl on restore. etcdctl
# matches the etcd the platform deploys. Everything is installed at build time
# so the Pods never run `apk add` and work on air-gapped clusters.
FROM quay.io/coreos/etcd:v3.6.11@sha256:6ae247c7666ceec554c51ba1f9bc8dd2212f975370dbd65710c9ca0e36ae1fff AS etcd

FROM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6

RUN apk add --no-cache curl jq ca-certificates coreutils

COPY --from=etcd /usr/local/bin/etcdctl /usr/local/bin/etcdctl

USER 65532:65532
