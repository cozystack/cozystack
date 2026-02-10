{{- define "clustersecret-operator.webhook.fullname" -}}
{{ include "clustersecret-operator.fullname" . }}-webhook
{{- end }}

{{- define "clustersecret-operator.webhook.labels" -}}
{{ include "clustersecret-operator.labels" . }}
app.kubernetes.io/component: webhook
{{- end }}

{{- define "clustersecret-operator.webhook.selectorLabels" -}}
{{ include "clustersecret-operator.selectorLabels" . }}
app.kubernetes.io/component: webhook
{{- end }}