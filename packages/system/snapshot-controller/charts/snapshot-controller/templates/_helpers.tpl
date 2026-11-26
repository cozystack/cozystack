{{/*
Expand the name of the chart.
*/}}
{{- define "snapshot-controller.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "snapshot-controller.fullname" -}}
{{- if .Values.controller.fullnameOverride -}}
{{- .Values.controller.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- if contains .Chart.Name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "snapshot-controller.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "snapshot-controller.labels" -}}
helm.sh/chart: {{ include "snapshot-controller.chart" . }}
{{ include "snapshot-controller.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "snapshot-controller.selectorLabels" -}}
app.kubernetes.io/name: {{ include "snapshot-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "snapshot-controller.serviceAccountName" -}}
{{- if .Values.controller.serviceAccount.create -}}
    {{ default (include "snapshot-controller.fullname" .) .Values.controller.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.controller.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
###############################
# Snapshot Conversion Webhook #
###############################
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "conversion-webhook.name" -}}
{{- "conversion-webhook" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "conversion-webhook.fullname" -}}
{{- if .Values.webhook.fullnameOverride -}}
{{- .Values.webhook.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- if contains "conversion-webhook" .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name "conversion-webhook" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "conversion-webhook.chart" -}}
{{- printf "%s-%s" "conversion-webhook" .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "conversion-webhook.labels" -}}
helm.sh/chart: {{ include "conversion-webhook.chart" . }}
{{ include "conversion-webhook.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "conversion-webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "conversion-webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "conversion-webhook.serviceAccountName" -}}
{{- if .Values.webhook.serviceAccount.create -}}
    {{ default (include "conversion-webhook.fullname" .) .Values.webhook.serviceAccount.name }}
{{- else -}}
    {{ default "default" .Values.webhook.serviceAccount.name }}
{{- end -}}
{{- end -}}

{{/*
Certificate secret name
*/}}
{{- define "conversion-webhook.certifcateName" -}}
{{- if .Values.webhook.tls.certificateSecret }}
{{- .Values.webhook.tls.certificateSecret }}
{{- else }}
{{- include "conversion-webhook.fullname" . }}
{{- end }}
{{- end }}
