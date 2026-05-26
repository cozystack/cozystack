{{/*
  Resolve the actual S3 bucket name backing the cozy-backups Bucket.

  The COSI driver (SeaweedFS) assigns its own bucket name on a per-claim
  basis; the value we configure on apps.cozystack.io/Bucket is only the
  *Kubernetes object name*, not the S3 bucket. Strategy CRs that hard-code
  `cozy-backups` produce a NoSuchBucket error against the real S3 endpoint.

  This helper looks up objectstorage.k8s.io/v1alpha1/BucketClaim
  `bucket-cozy-backups` (the bucket-rd "bucket-" prefix wraps the
  apps.cozystack.io/Bucket release name) in the backup-controller
  namespace and reads `.status.bucketName` — the authoritative S3 bucket
  name the COSI driver chose. The result is consumed by every strategy
  template (CNPG/Etcd/MariaDB/FDB) and by the Velero BackupStorageLocation.

  Failure semantics:
    - provisionBucket: false → the admin manages the source Secret
      directly, and `.Values.backupStorage.bucketName` is authoritative.
      Return it as-is.
    - provisionBucket: true + BucketClaim missing / status not populated →
      `required` to fail the Helm render. Flux retries the HelmRelease,
      eventually reading a populated status. dependsOn on
      cozystack.bucket-application + cozystack.objectstorage-controller
      ensures the controllers exist before this chart installs, but the
      Secret/Status is reconciled asynchronously so the first render may
      still race; a retry settles it.
    - lookup returns nil during `helm template` / `helm install --dry-run`
      (no apiserver to query) — fall back to chart values so render works
      offline. Real deploys go through Flux which uses live lookup.
*/}}
{{- define "backupstrategy-controller.bucketName" -}}
{{- $configured := .Values.backupStorage.bucketName -}}
{{- if not .Values.backupStorage.provisionBucket -}}
{{/* External S3: .Values.backupStorage.bucketName is authoritative. */}}
{{- $configured -}}
{{- else -}}
{{- $bucketClaim := lookup "objectstorage.k8s.io/v1alpha1" "BucketClaim" .Values.backupStorage.namespace (printf "bucket-%s" $configured) -}}
{{- if and $bucketClaim $bucketClaim.status (index $bucketClaim.status "bucketName") -}}
{{- index $bucketClaim.status "bucketName" -}}
{{- else if .Values.backupStorage.bucketNameOverride -}}
{{/* Offline render / pre-reconcile install: admin opted out of the
     BucketClaim lookup by overriding the bucket name directly. */}}
{{- .Values.backupStorage.bucketNameOverride -}}
{{- end -}}
{{/* When neither path produces a value, emit the empty string.
     Strategy/BSL templates that include this helper must gate
     themselves on a non-empty result and skip rendering until Flux
     re-reconciles the HelmRelease (driven by spec.interval) once the
     BucketClaim's COSI-assigned status.bucketName is populated. The
     accompanying templates/bucket.yaml ALWAYS renders so the
     BucketClaim CAN come into existence even on the first install. */}}
{{- end -}}
{{- end -}}
