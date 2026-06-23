{{/* vim: set filetype=mustache: */}}
{{/*
Create a default fully qualified app name.
*/}}
{{- define "vertical-pod-autoscaler.tests.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.fullname" .) "tests" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Component labels
*/}}
{{- define "vertical-pod-autoscaler.tests.componentLabels" -}}
app.kubernetes.io/component: tests
{{- end -}}

{{/*
Labels
*/}}
{{- define "vertical-pod-autoscaler.tests.labels" -}}
{{ include "vertical-pod-autoscaler.commonLabels" . }}
{{ include "vertical-pod-autoscaler.tests.componentLabels" . }}
{{- end -}}
