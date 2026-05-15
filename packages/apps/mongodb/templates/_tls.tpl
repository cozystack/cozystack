{{/*
Tri-state TLS resolver.

Returns "true" when TLS should be active, empty string otherwise.

Resolution order:
  1. If tls.enabled is explicitly set (boolean), use that value.
  2. If tls.enabled is unset (null / invalid), inherit .Values.external.

Usage:
  {{- $tlsEnabled := include "mongodb.tls.enabled" . | eq "true" -}}
*/}}
{{- define "mongodb.tls.enabled" -}}
{{- if kindIs "invalid" .Values.tls.enabled -}}
  {{- .Values.external | default false -}}
{{- else -}}
  {{- .Values.tls.enabled -}}
{{- end -}}
{{- end -}}
