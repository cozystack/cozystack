{{/*
Expand the name of the chart.
*/}}
{{- define "cozy-tls.name" -}}
{{- .Release.Name }}
{{- end }}

{{/*
Name for the self-signed bootstrap issuer.
*/}}
{{- define "cozy-tls.selfsigned-issuer-name" -}}
{{- printf "%s-selfsigned" .Release.Name }}
{{- end }}

{{/*
Name for the CA certificate resource.
*/}}
{{- define "cozy-tls.ca-name" -}}
{{- printf "%s-ca" .Release.Name }}
{{- end }}

{{/*
Name of the Secret holding the CA key-pair.
*/}}
{{- define "cozy-tls.ca-secret-name" -}}
{{- printf "%s-ca" .Release.Name }}
{{- end }}

{{/*
Name for the leaf certificate resource.
*/}}
{{- define "cozy-tls.certificate-name" -}}
{{- printf "%s-tls" .Release.Name }}
{{- end }}

{{/*
Name of the Secret holding the leaf certificate.
Falls back to "<release>-tls" when .Values.certificate.secretName is empty.
*/}}
{{- define "cozy-tls.certificate-secret-name" -}}
{{- if .Values.certificate.secretName -}}
{{- .Values.certificate.secretName }}
{{- else -}}
{{- printf "%s-tls" .Release.Name }}
{{- end }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "cozy-tls.labels" -}}
app.kubernetes.io/name: {{ include "cozy-tls.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
