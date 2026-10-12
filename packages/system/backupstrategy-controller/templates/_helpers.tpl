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

{{/*
  Answer, for one CRD this chart ships as a template, whether the apiserver
  already serves its kind.

  Why a default Strategy needs this on top of the bucket-name gate. This chart
  ships its strategy.backups.cozystack.io CRDs as ordinary templates
  (templates/crds.yaml globs definitions/*.yaml), and Helm resolves EVERY
  document in a release manifest through the cluster's RESTMapper in Build()
  before it applies any of them. A Strategy CR whose CRD is not served yet
  therefore fails Build(), and the release applies ZERO objects — including the
  CRDs that would have made the mapping resolve. Every retry renders the same
  manifest, so the failure is permanent rather than a race.

  The bucket-name gate covers the FIRST INSTALL of a new cluster, where nothing
  is resolvable yet and no Strategy renders at all. It cannot cover the case
  this helper exists for: adding a NEW strategy kind to a chart that is already
  installed. There the bucket name resolved long ago, so the first upgraded
  revision renders the new CRD and the new Strategy CR together and wedges the
  release exactly as above. That is not hypothetical — it is how the MongoDB
  driver reached main, and the upgrade E2E lane caught it on the v1.6.2 → main
  path with `no matches for kind "MongoDB"`.

  Asking about the CRD OBJECT rather than listing the custom kind is
  deliberate. `lookup` on a kind the apiserver does not serve is not an empty
  result — it is a render ERROR ("unable to get apiresource"), which would trade
  the wedge for a different permanent failure. apiextensions.k8s.io/v1 is always
  served, so this lookup can only answer yes or no. Established=True rather than
  mere existence, because a CRD is only in the RESTMapper once it is; a
  not-yet-established CRD costs one retried revision here instead of a failed
  one, and Flux retries.

  Convergence after a skip is the same mechanism the bucket gate relies on and
  is NOT the HelmRelease interval (see the bucketName helper): once the CRD is
  served, the controller's DefaultObjectsGate sees the kind mapped and the CR
  absent and forces a real Helm upgrade, whose render then includes it. The gate
  skips kinds that do not map at all (meta.IsNoMatchError), which is what keeps
  it from forcing upgrades against a render that cannot yet produce the object.

  bucketNameOverride short-circuits the lookup for the same reason it does in
  bucketName: it marks an offline render — `helm template`, a CI diff, a
  helm-unittest case — where there is no apiserver to ask and the operator has
  declared that cluster-derived state should be treated as resolved. Live
  deploys go through Flux and never set it.

  Every default Strategy added from here on should gate on this helper as well
  as on the bucket name. The siblings that predate it are not wedged today only
  because their CRDs already shipped in an earlier release.
*/}}
{{- define "backupstrategy-controller.crdEstablished" -}}
{{- if .root.Values.backupStorage.bucketNameOverride -}}
true
{{- else -}}
{{- $crd := lookup "apiextensions.k8s.io/v1" "CustomResourceDefinition" "" .crd -}}
{{- if and $crd $crd.status -}}
{{- range $c := (default (list) $crd.status.conditions) -}}
{{- if and (eq $c.type "Established") (eq (toString $c.status) "True") -}}
true
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
