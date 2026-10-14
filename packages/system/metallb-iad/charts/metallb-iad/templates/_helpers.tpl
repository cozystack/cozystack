{{/* Common metadata labels (not used as selectors — selectors stay stable). */}}
{{- define "metallb-iad.labels" -}}
app.kubernetes.io/name: metallb-iad
app.kubernetes.io/part-of: metallb-iad
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "metallb-iad-%s" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* The namespace reservation placeholders live in (defaults to the release namespace). */}}
{{- define "metallb-iad.placeholderNamespace" -}}
{{- default .Release.Namespace .Values.placeholderNamespace -}}
{{- end -}}

{{/* imagePullSecrets block, rendered only when set. Call with the root context. */}}
{{- define "metallb-iad.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
