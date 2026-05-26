{{/*
Determines whether TLS should be enabled.
Returns "true" or "false" (string).
When tls.enabled is explicitly set, its value is used.
When tls.enabled is unset (null/invalid), falls back to .Values.external.
*/}}
{{- define "qdrant.tls.enabled" -}}
{{- if kindIs "invalid" .Values.tls.enabled -}}
{{- .Values.external | default false -}}
{{- else -}}
{{- .Values.tls.enabled -}}
{{- end -}}
{{- end -}}
