{{/* Common metadata labels (not used as selectors — selectors stay stable). */}}
{{- define "cozyplane-apiserver.labels" -}}
app.kubernetes.io/name: cozyplane-apiserver
app.kubernetes.io/part-of: cozyplane
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "cozyplane-apiserver-%s" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* imagePullSecrets block, rendered only when set. Call with the root context. */}}
{{- define "cozyplane-apiserver.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
