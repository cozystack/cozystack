{{/* vim: set filetype=mustache: */}}
{{/*
Create a default fully qualified app name.
*/}}
{{- define "vertical-pod-autoscaler.updater.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.fullname" .) "updater" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified metrics name.
*/}}
{{- define "vertical-pod-autoscaler.updater.metrics.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.updater.fullname" .) "metrics" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Component labels
*/}}
{{- define "vertical-pod-autoscaler.updater.componentLabels" -}}
app.kubernetes.io/component: updater
{{- end -}}

{{/*
Labels
*/}}
{{- define "vertical-pod-autoscaler.updater.labels" -}}
{{ include "vertical-pod-autoscaler.commonLabels" . }}
{{ include "vertical-pod-autoscaler.updater.componentLabels" . }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "vertical-pod-autoscaler.updater.selectorLabels" -}}
{{ include "vertical-pod-autoscaler.commonSelectorLabels" . }}
{{ include "vertical-pod-autoscaler.updater.componentLabels" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "vertical-pod-autoscaler.updater.serviceAccountName" -}}
{{- if .Values.updater.serviceAccount.create -}}
    {{ default (include "vertical-pod-autoscaler.updater.fullname" .) .Values.updater.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.updater.serviceAccount.name }}
{{- end -}}
{{- end -}}
