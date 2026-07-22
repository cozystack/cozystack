{{- /*
cozy-lib.barman.checksumSidecarConfiguration renders a barman-cloud ObjectStore
`spec.instanceSidecarConfiguration` that pins the sidecar's boto3 request-checksum
policy to when_required.

Since botocore ~1.36 (early 2025) the default RequestChecksumCalculation is
when_supported, which attaches a flexible checksum (the x-amz-content-sha256
header) to every PutObject. Non-AWS S3-compatible backends (Ceph RADOS Gateway,
the platform's own SeaweedFS system bucket, some MinIO/Cloudflare R2 builds)
reject it with "InvalidArgument: x-amz-content-sha256 must be UNSIGNED-PAYLOAD,
...", so every backup/WAL-archive upload fails against them. when_required
computes a checksum only when the operation mandates one; AWS S3 accepts that on
a plain PutObject too, so it is a safe default everywhere.

Emit under an ObjectStore `spec:` with `{{- include "cozy-lib.barman.checksumSidecarConfiguration" . | nindent 2 }}`.
The Go-driven platform (useSystemBucket=true) ObjectStore sets the same env in
internal/backupcontroller/cnpgstrategy_controller.go (barmanSidecarConfiguration).
*/ -}}
{{- define "cozy-lib.barman.checksumSidecarConfiguration" -}}
instanceSidecarConfiguration:
  env:
    - name: AWS_REQUEST_CHECKSUM_CALCULATION
      value: when_required
{{- end -}}
