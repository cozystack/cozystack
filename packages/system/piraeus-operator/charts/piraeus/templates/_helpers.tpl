{{/*
Expand the name of the chart.
*/}}
{{- define "piraeus-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "piraeus-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "piraeus-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "piraeus-operator.labels" -}}
helm.sh/chart: {{ include "piraeus-operator.chart" . }}
{{ include "piraeus-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "piraeus-operator.selectorLabels" -}}
app.kubernetes.io/component: piraeus-operator
app.kubernetes.io/name: piraeus-datastore
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "piraeus-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "piraeus-operator.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Certificate secret name
*/}}
{{- define "piraeus-operator.certifcateName" -}}
{{- if .Values.tls.certificateSecret }}
{{- .Values.tls.certificateSecret }}
{{- else }}
{{- include "piraeus-operator.fullname" . }}-tls
{{- end }}
{{- end }}
