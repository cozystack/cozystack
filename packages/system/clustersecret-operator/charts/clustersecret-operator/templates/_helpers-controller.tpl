{{- define "clustersecret-operator.controller.fullname" -}}
{{ include "clustersecret-operator.fullname" . }}-controller
{{- end }}

{{- define "clustersecret-operator.controller.labels" -}}
{{ include "clustersecret-operator.labels" . }}
app.kubernetes.io/component: controller
{{- end }}

{{- define "clustersecret-operator.controller.selectorLabels" -}}
{{ include "clustersecret-operator.selectorLabels" . }}
app.kubernetes.io/component: controller
{{- end }}