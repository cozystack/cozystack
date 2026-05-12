{{/*
redis.tls.enabled — check if TLS is enabled via cozy-tls sub-chart.
*/}}
{{- define "redis.tls.enabled" -}}
{{- .Values.tls.enabled -}}
{{- end -}}

{{/*
redis.tls.secretName — return the TLS secret name from cozy-tls sub-chart config.
Falls back to "<release>-tls" when certificate.secretName is empty.
*/}}
{{- define "redis.tls.secretName" -}}
{{- default (printf "%s-tls" .Release.Name) .Values.tls.certificate.secretName -}}
{{- end -}}
