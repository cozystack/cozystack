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
      sees an unpopulated status and skips. Flux re-renders the
      HelmRelease on its next reconcile (spec.interval); once COSI has
      populated status.bucketName, the gated templates materialise.
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
     themselves on a non-empty result and skip rendering until Flux
     re-reconciles the HelmRelease (driven by spec.interval) once the
     BucketClaim's COSI-assigned status.bucketName is populated. The
     accompanying templates/bucket.yaml ALWAYS renders so the
     BucketClaim CAN come into existence even on the first install. */}}
{{- end -}}
{{- end -}}

{{/*
  Resolve the S3 endpoint URL (scheme included) shared by every default
  Strategy CR (CNPG/Etcd/MariaDB/FDB), the Velero BSL, and the controller
  Deployment env.

  Why not just use .Values.backupStorage.endpoint: Cozystack ships SeaweedFS
  with global.seaweedfs.enableSecurity=true, so its in-cluster S3 Service
  serves TLS on :8333 fronted by the self-signed "SeaweedFS CA". The static
  default endpoint (http://seaweedfs-s3...svc:8333) therefore hits a TLS
  listener over plaintext and every backup upload fails the handshake. The
  Etcd Strategy's S3 schema has no caCert/insecureSkipVerify field, so it
  cannot target the self-signed in-cluster endpoint at all — it needs a
  trusted-cert endpoint. The COSI-provisioned bucket exposes exactly that: the
  external S3 ingress (ACME cert), advertised in the bucket's system
  credentials Secret (backupStorage.systemSecretName) — the same Secret the
  projector already consumes. We read the bucket host from there and force the
  https:// scheme (the S3 ingress is always TLS).

  Failure semantics mirror bucketName:
    - provisionBucket: false → external S3, the admin-configured
      .Values.backupStorage.endpoint is authoritative. Return it as-is.
    - provisionBucket: true + system Secret present → return
      https://<bucket-host>.
    - provisionBucket: true + Secret missing (offline `helm template`/unit
      render, or pre-reconcile install where lookup returns nil) → fall back
      to .Values.backupStorage.endpoint. On a live deploy Flux re-renders on
      spec.interval once the Secret exists, promoting the derived endpoint.

  Normalization of the decoded value: trim first (a trailing newline in the
  Secret data would otherwise ride into the URL and break it), then strip any
  leftover scheme before re-forcing https://. The producer already writes a
  bare host, so the trimPrefix pair is belt-and-suspenders and never fires
  today — kept as insurance against a future producer that emits a scheme.
*/}}
{{- define "backupstrategy-controller.endpoint" -}}
{{- if not .Values.backupStorage.provisionBucket -}}
{{- .Values.backupStorage.endpoint -}}
{{- else -}}
{{- $secret := lookup "v1" "Secret" .Values.backupStorage.namespace .Values.backupStorage.systemSecretName -}}
{{- if and $secret $secret.data (index $secret.data "endpoint") -}}
{{- printf "https://%s" (b64dec (index $secret.data "endpoint") | trim | trimPrefix "https://" | trimPrefix "http://") -}}
{{- else -}}
{{- .Values.backupStorage.endpoint -}}
{{- end -}}
{{- end -}}
{{- end -}}
