{{/*
Expand the name of the chart.
*/}}
{{- define "flux-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "flux-operator.fullname" -}}
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
{{- define "flux-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "flux-operator.labels" -}}
helm.sh/chart: {{ include "flux-operator.chart" . }}
{{ include "flux-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "flux-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "flux-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "flux-operator.serviceAccountName" -}}
{{- default (include "flux-operator.fullname" .) .Values.serviceAccount.name }}
{{- end }}

{{/*
Check if web config secret should be created.
Returns "true" when web is enabled, config is provided inline, and no external secret is referenced.
*/}}
{{- define "flux-operator.createWebConfigSecret" -}}
{{- if and .Values.web.enabled .Values.web.config (not .Values.web.configSecretName) -}}
true
{{- end -}}
{{- end }}

{{/*
Check if an external web config secret is referenced.
Returns "true" when web is enabled, an external secret name is provided, and no inline config is set.
*/}}
{{- define "flux-operator.useWebConfigSecret" -}}
{{- if and .Values.web.enabled .Values.web.configSecretName (not .Values.web.config) -}}
true
{{- end -}}
{{- end }}

{{/*
Check if running in web server only mode.
Returns "true" when web is enabled and serverOnly mode is set.
*/}}
{{- define "flux-operator.webServerOnly" -}}
{{- if and .Values.web.enabled .Values.web.serverOnly -}}
true
{{- end -}}
{{- end }}

{{/*
Check if web server replicas should be set.
Returns "true" when web is enabled, serverOnly mode is set, and serverReplicas is greater than 1.
*/}}
{{- define "flux-operator.setWebServerReplicas" -}}
{{- if and .Values.web.enabled .Values.web.serverOnly (gt (int .Values.web.serverReplicas) 1) -}}
true
{{- end -}}
{{- end }}

{{/*
Check if web roles should be created.
Returns "true" when web is enabled and createRoles is enabled.
*/}}
{{- define "flux-operator.createWebRoles" -}}
{{- if and .Values.web.enabled .Values.web.rbac.createRoles -}}
true
{{- end -}}
{{- end }}

{{/*
Check if web roles aggregation should be created.
Returns "true" when web is enabled and createAggregation is enabled.
*/}}
{{- define "flux-operator.createWebRolesAggregation" -}}
{{- if and .Values.web.enabled .Values.web.rbac.createAggregation -}}
true
{{- end -}}
{{- end }}
