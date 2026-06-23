{{/* vim: set filetype=mustache: */}}
{{/*
Create a default fully qualified app name.
*/}}
{{- define "vertical-pod-autoscaler.recommender.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.fullname" .) "recommender" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified metrics name.
*/}}
{{- define "vertical-pod-autoscaler.recommender.metrics.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.recommender.fullname" .) "metrics" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Component labels
*/}}
{{- define "vertical-pod-autoscaler.recommender.componentLabels" -}}
app.kubernetes.io/component: recommender
{{- end -}}

{{/*
Labels
*/}}
{{- define "vertical-pod-autoscaler.recommender.labels" -}}
{{ include "vertical-pod-autoscaler.commonLabels" . }}
{{ include "vertical-pod-autoscaler.recommender.componentLabels" . }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "vertical-pod-autoscaler.recommender.selectorLabels" -}}
{{ include "vertical-pod-autoscaler.commonSelectorLabels" . }}
{{ include "vertical-pod-autoscaler.recommender.componentLabels" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "vertical-pod-autoscaler.recommender.serviceAccountName" -}}
{{- if .Values.recommender.serviceAccount.create -}}
    {{ default (include "vertical-pod-autoscaler.recommender.fullname" .) .Values.recommender.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.recommender.serviceAccount.name }}
{{- end -}}
{{- end -}}
