{{/*
Determines whether TLS should be enabled.
Returns "true" or "false" (string).
Inherits .Values.external when .Values.tls.enabled is not set.
*/}}
{{- define "qdrant.tls.enabled" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- if hasKey $tlsMap "enabled" -}}
{{- index $tlsMap "enabled" -}}
{{- else -}}
{{- .Values.external | default false -}}
{{- end -}}
{{- end -}}
