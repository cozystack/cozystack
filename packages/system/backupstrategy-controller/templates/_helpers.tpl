{{/*
  Resolve the actual S3 bucket name backing the cozy-backups Bucket.

  The COSI driver (SeaweedFS) assigns its own bucket name on a per-claim
  basis; the value we configure on apps.cozystack.io/Bucket is only the
  *Kubernetes object name*, not the S3 bucket. Strategy CRs that hard-code
  `cozy-backups` produce a NoSuchBucket error against the real S3 endpoint.

  This helper looks up objectstorage.k8s.io/v1alpha1/BucketClaim
  `bucket-cozy-backups` (the bucket-rd "bucket-" prefix wraps the
  apps.cozystack.io/Bucket release name) in the `backupStorage.namespace`
  (tenant-root by default) and reads `.status.bucketName` — the authoritative S3 bucket
  name the COSI driver chose. The result is consumed by every strategy
  template (CNPG/Etcd/MariaDB/FDB) and by the Velero BackupStorageLocation.

  Failure semantics:
    - provisionBucket: false → the admin manages the source Secret
      directly, and `.Values.backupStorage.bucketName` is authoritative.
      Return it as-is.
    - provisionBucket: true + BucketClaim status populated → return the
      COSI-assigned `.status.bucketName`.
    - provisionBucket: true + BucketClaim missing / status not populated →
      emit the empty string (NO `required`, NO render failure). The
      Strategy and Velero BSL templates gate on a non-empty result and
      skip rendering, while templates/bucket.yaml ALWAYS renders so the
      BucketClaim CAN be created on the first install. dependsOn on
      cozystack.bucket-application + cozystack.objectstorage-controller
      ensures the controllers exist before this chart installs, but the
      BucketClaim status is reconciled asynchronously, so the first render
      sees an unpopulated status and skips.

      The render CANNOT fail here instead: this chart is the producer of
      the very Bucket the lookup reads, so a failed render would never
      apply templates/bucket.yaml and the condition could never resolve.

      Nor does Flux repair the skip on its own. helm-controller does not
      re-render a release whose chart and values did not change — the
      interval reconcile is a no-op for a healthy release, and drift
      detection is off on operator-generated HelmReleases — so the skip is
      PERMANENT, not a bootstrap window. Convergence is driven instead by
      the controller's DefaultObjectsGate
      (internal/backupcontroller/default_objects_gate.go), which forces a
      real Helm upgrade (reconcile.fluxcd.io/forceAt + requestedAt) once
      the bucket name is resolvable and any gated object is missing. See
      backupStorage.reconcileDefaultObjects in values.yaml.
    - bucketNameOverride set → bypass the lookup and use it directly. This
      is the escape hatch for offline `helm template` / `--dry-run` renders
      (CI / local diffs), where lookup returns nil and no apiserver is
      reachable. When lookup is nil AND no override is set, the helper
      emits the empty string (the skip-render path above). Real deploys go
      through Flux, which uses a live lookup and needs no override.
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
     themselves on a non-empty result and skip rendering until a real
     Helm upgrade re-runs this lookup with the BucketClaim's
     COSI-assigned status.bucketName populated. That upgrade is forced by
     the controller's DefaultObjectsGate — it does NOT happen on the
     HelmRelease's interval reconcile. The accompanying
     templates/bucket.yaml ALWAYS renders so the BucketClaim CAN come
     into existence even on the first install. */}}
{{- end -}}
{{- end -}}
