{{/*
Expand the name of the chart.
*/}}
{{- define "kamaji.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kamaji.fullname" -}}
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
{{- define "kamaji.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kamaji.labels" -}}
helm.sh/chart: {{ include "kamaji.chart" . }}
{{ include "kamaji.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kamaji.selectorLabels" -}}
app.kubernetes.io/name: {{ default (include "kamaji.name" .) .name }}
app.kubernetes.io/instance: {{ default .Release.Name .instance }}
app.kubernetes.io/component: {{ default "controller-manager" .component }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "kamaji.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kamaji.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the Service to user for webhooks
*/}}
{{- define "kamaji.webhookServiceName" -}}
{{- printf "%s-webhook-service" (include "kamaji.fullname" .) }}
{{- end }}

{{/*
Create the name of the Service to user for metrics
*/}}
{{- define "kamaji.metricsServiceName" -}}
{{- printf "%s-metrics-service" (include "kamaji.fullname" .) }}
{{- end }}

{{/*
Create the name of the cert-manager secret
*/}}
{{- define "kamaji.webhookSecretName" -}}
{{- printf "%s-webhook-server-cert" (include "kamaji.fullname" .) }}
{{- end }}

{{/*
Create the name of the cert-manager Certificate
*/}}
{{- define "kamaji.certificateName" -}}
{{- printf "%s-serving-cert" (include "kamaji.fullname" .) }}
{{- end }}


{{/*
Kubeconfig Generator Deployment name.
*/}}
{{- define "kamaji.kubeconfigGeneratorName" -}}
{{- if .Values.kubeconfigGenerator.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name "kubeconfig-generator" | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
