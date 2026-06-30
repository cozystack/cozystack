{{/* Common metadata labels (not used as selectors — selectors stay stable). */}}
{{- define "cozyplane.labels" -}}
app.kubernetes.io/name: cozyplane
app.kubernetes.io/part-of: cozyplane
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: cozyplane-{{ .Chart.Version }}
{{- end -}}

{{/* imagePullSecrets block, rendered only when set. Call with the root context. */}}
{{- define "cozyplane.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
