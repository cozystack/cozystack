{{/* Common metadata labels (not used as selectors — selectors stay stable). */}}
{{- define "address-controller.labels" -}}
app.kubernetes.io/name: address-controller
app.kubernetes.io/part-of: address-controller
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "address-controller-%s" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{/* imagePullSecrets block, rendered only when set. Call with the root context. */}}
{{- define "address-controller.imagePullSecrets" -}}
{{- with .Values.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end -}}
