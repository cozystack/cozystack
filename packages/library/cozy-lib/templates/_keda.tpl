{{/*
cozy-lib.keda.scaledObject renders a KEDA ScaledObject that drives an engine
operator's /scale subresource from a single VictoriaMetrics query.

This is the whole runtime of database read-replica autoscaling in cozystack:
the app chart renders this object, and stock KEDA queries VictoriaMetrics,
computes the desired count with an HPA it manages, and writes the engine CR's
scale subresource. There is no bespoke controller (see the design proposal
"database-horizontal-autoscaling"). The helper is engine-agnostic: the caller
supplies the already-computed effective bounds (the synchronous-quorum floor is
engine-specific and belongs in the chart, not here), the scale target, and the
metric query.

Invoked with a single dict argument:

  {{ include "cozy-lib.keda.scaledObject" (dict
       "name"            .Release.Name
       "namespace"       .Release.Namespace
       "scaleTargetRef"  (dict "apiVersion" "postgresql.cnpg.io/v1" "kind" "Cluster" "name" .Release.Name)
       "minReplicaCount" $effectiveMin
       "maxReplicaCount" $effectiveMax
       "serverAddress"   $vmselectURL
       "query"           $promql
       "threshold"       "150"
       "behavior"        $hpaBehavior      # optional; HPA behavior block, carried verbatim
       "paused"          $paused           # true to render paused; a caller pauses for BOTH dry-run AND migration transition, not dry-run alone
       "labels"          (dict "app.kubernetes.io/instance" .Release.Name)
  ) }}

Parameters:
  - name            (required) ScaledObject name. Convention: the release name.
  - scaleTargetRef  (required) dict with apiVersion, kind, name of the CR to scale.
  - minReplicaCount (required) effective lower bound (chart computes the quorum floor).
  - maxReplicaCount (required) effective upper bound; must be >= minReplicaCount.
  - serverAddress   (required) Prometheus-API URL of the tenant's vmselect.
  - query           (required) PromQL whose value / threshold is the desired count.
  - threshold       (required) AverageValue target; desired = ceil(query / threshold).
  - namespace       (optional) metadata.namespace.
  - behavior        (optional) HPA behavior block (scale-down pacing etc.), carried verbatim.
  - pollingInterval (optional) KEDA polling interval, seconds.
  - cooldownPeriod  (optional) KEDA cooldown period, seconds.
  - paused          (optional) when true, stamps autoscaling.keda.sh/paused=true
                    (dry-run / recommendation mode — KEDA does not actuate).
  - labels          (optional) extra labels, merged onto the mandatory ones.
  - annotations     (optional) extra annotations, merged under the pause annotation.
*/}}
{{- define "cozy-lib.keda.scaledObject" -}}
{{-   if not (kindIs "map" .) -}}
{{-     fail "cozy-lib.keda.scaledObject: expected a single dict argument" -}}
{{-   end -}}
{{-   $name := default "" .name -}}
{{-   if eq $name "" -}}
{{-     fail "cozy-lib.keda.scaledObject: name is required" -}}
{{-   end -}}
{{-   $ref := default (dict) .scaleTargetRef -}}
{{-   if not (kindIs "map" $ref) -}}
{{-     fail "cozy-lib.keda.scaledObject: scaleTargetRef must be a dict" -}}
{{-   end -}}
{{-   if eq (default "" $ref.name) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: scaleTargetRef.name is required" -}}
{{-   end -}}
{{-   if eq (default "" $ref.kind) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: scaleTargetRef.kind is required" -}}
{{-   end -}}
{{-   if eq (default "" $ref.apiVersion) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: scaleTargetRef.apiVersion is required" -}}
{{-   end -}}
{{-   if eq (printf "%v" (default "" .query)) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: query is required" -}}
{{-   end -}}
{{-   if eq (printf "%v" (default "" .threshold)) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: threshold is required" -}}
{{-   end -}}
{{-   if eq (default "" .serverAddress) "" -}}
{{-     fail "cozy-lib.keda.scaledObject: serverAddress is required" -}}
{{-   end -}}
{{- /* Bounds are required integers; the caller has already folded the quorum floor
       into them, so all this helper enforces is the ordering invariant. */ -}}
{{-   if kindIs "invalid" .minReplicaCount -}}
{{-     fail "cozy-lib.keda.scaledObject: minReplicaCount is required" -}}
{{-   end -}}
{{-   if kindIs "invalid" .maxReplicaCount -}}
{{-     fail "cozy-lib.keda.scaledObject: maxReplicaCount is required" -}}
{{-   end -}}
{{-   $min := int .minReplicaCount -}}
{{-   $max := int .maxReplicaCount -}}
{{-   if lt $max $min -}}
{{-     fail "cozy-lib.keda.scaledObject: maxReplicaCount must be >= minReplicaCount" -}}
{{-   end -}}
{{-   $labels := merge (dict "app.kubernetes.io/managed-by" "cozystack") (default (dict) .labels) -}}
{{-   $annotations := default (dict) .annotations -}}
{{-   if .paused -}}
{{-     $annotations = merge (dict "autoscaling.keda.sh/paused" "true") $annotations -}}
{{-   end -}}
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: {{ $name }}
{{-   with .namespace }}
  namespace: {{ . }}
{{-   end }}
  labels: {{- toYaml $labels | nindent 4 }}
{{-   if $annotations }}
  annotations: {{- toYaml $annotations | nindent 4 }}
{{-   end }}
spec:
  scaleTargetRef:
    apiVersion: {{ $ref.apiVersion }}
    kind: {{ $ref.kind }}
    name: {{ $ref.name }}
  minReplicaCount: {{ $min }}
  maxReplicaCount: {{ $max }}
{{-   with .pollingInterval }}
  pollingInterval: {{ . }}
{{-   end }}
{{-   with .cooldownPeriod }}
  cooldownPeriod: {{ . }}
{{-   end }}
{{-   with .behavior }}
  advanced:
    horizontalPodAutoscalerConfig:
      behavior: {{- toYaml . | nindent 8 }}
{{-   end }}
  triggers:
    - type: prometheus
      {{- /* Explicit, not relying on KEDA's default: the desired-count identity
             desired = ceil(query / threshold) holds only for an AverageValue
             external metric (no per-pod divisor). Pinning it keeps a KEDA
             re-vendor or default change from silently breaking the arithmetic. */}}
      metricType: AverageValue
      metadata:
        serverAddress: {{ .serverAddress | quote }}
        query: {{ .query | quote }}
        threshold: {{ .threshold | quote }}
{{- end -}}
