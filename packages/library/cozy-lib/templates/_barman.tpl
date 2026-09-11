{{- /*
cozy-lib.barman.sidecarConfiguration renders a barman-cloud ObjectStore
`spec.instanceSidecarConfiguration` shared by every chart that enables the
plugin. It carries the two settings the sidecar cannot get right on its own.

Resources: tenant namespaces ship a LimitRange defaulting containers to 128Mi
(packages/apps/tenant/templates/quota.yaml). The plugin injects the sidecar
without resources, so it inherits that default and is OOMKilled mid-backup,
leaving the ObjectStore healthy and the backup failed. Measured cgroup
high-water mark on a 38 MB database is 254 MiB, so 128Mi cannot hold it. No CPU
limit is set, matching the entityOperator precedent in packages/apps/kafka: a
throttled sidecar stalls WAL archiving instead of failing it.

Checksum: since botocore ~1.36 (early 2025) the default
RequestChecksumCalculation is when_supported, which attaches a flexible checksum
(the x-amz-content-sha256 header) to every PutObject. Non-AWS S3-compatible
backends (Ceph RADOS Gateway, the platform's own SeaweedFS system bucket, some
MinIO/Cloudflare R2 builds) reject it with "InvalidArgument:
x-amz-content-sha256 must be UNSIGNED-PAYLOAD, ...", so every backup/WAL-archive
upload fails against them. when_required computes a checksum only when the
operation mandates one; AWS S3 accepts that on a plain PutObject too, so it is a
safe default everywhere.

Emit under an ObjectStore `spec:` with `{{- include "cozy-lib.barman.sidecarConfiguration" . | nindent 2 }}`.
The Go-driven platform (useSystemBucket=true) ObjectStore sets the same fields in
internal/backupcontroller/cnpgstrategy_controller.go (barmanSidecarConfiguration).
*/ -}}
{{- define "cozy-lib.barman.sidecarConfiguration" -}}
instanceSidecarConfiguration:
  resources:
    requests:
      cpu: 100m
      memory: 256Mi
    limits:
      memory: 1Gi
  env:
    - name: AWS_REQUEST_CHECKSUM_CALCULATION
      value: when_required
{{- end -}}
