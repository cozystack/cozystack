{{/* vim: set filetype=mustache: */}}
{{/*
Create a default fully qualified app name.
*/}}
{{- define "vertical-pod-autoscaler.admissionController.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.fullname" .) "admission-controller" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Create a default fully qualified metrics name.
*/}}
{{- define "vertical-pod-autoscaler.admissionController.metrics.fullname" -}}
{{- printf "%s-%s" (include "vertical-pod-autoscaler.admissionController.fullname" .) "metrics" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Component labels
*/}}
{{- define "vertical-pod-autoscaler.admissionController.componentLabels" -}}
app.kubernetes.io/component: admission-controller
{{- end -}}

{{/*
Labels
*/}}
{{- define "vertical-pod-autoscaler.admissionController.labels" -}}
{{ include "vertical-pod-autoscaler.commonLabels" . }}
{{ include "vertical-pod-autoscaler.admissionController.componentLabels" . }}
{{- end -}}

{{/*
Selector labels
*/}}
{{- define "vertical-pod-autoscaler.admissionController.selectorLabels" -}}
{{ include "vertical-pod-autoscaler.commonSelectorLabels" . }}
{{ include "vertical-pod-autoscaler.admissionController.componentLabels" . }}
{{- end -}}

{{/*
Create the name of the service account to use
*/}}
{{- define "vertical-pod-autoscaler.admissionController.serviceAccountName" -}}
{{- if .Values.admissionController.serviceAccount.create -}}
    {{ default (include "vertical-pod-autoscaler.admissionController.fullname" .) .Values.admissionController.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.admissionController.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Create the name of the tls secret to use
*/}}
{{- define "vertical-pod-autoscaler.admissionController.tls.secretName" -}}
{{- if .Values.admissionController.tls.existingSecret -}}
    {{ .Values.admissionController.tls.existingSecret }}
{{- else -}}
    {{- printf "%s-%s" (include "vertical-pod-autoscaler.admissionController.fullname" .) "tls" | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
