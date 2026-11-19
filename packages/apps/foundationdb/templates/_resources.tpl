{{/*
Common resource definitions
*/}}
{{- define "foundationdb.resources" -}}
{{- include "cozy-lib.resources.defaultingSanitize" (list .Values.resources.preset .Values.resources $) }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "foundationdb.labels" -}}
helm.sh/chart: {{ include "foundationdb.chart" . }}
{{ include "foundationdb.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "foundationdb.selectorLabels" -}}
app.kubernetes.io/name: foundationdb
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Chart name and version
*/}}
{{- define "foundationdb.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Calculate minReplicas for WorkloadMonitor based on redundancyMode
*/}}
{{- define "foundationdb.minReplicas" -}}
{{- $replicas := .Values.cluster.processCounts.storage -}}
{{- if or (eq .Values.cluster.redundancyMode "triple") (eq .Values.cluster.redundancyMode "three_data_hall") (eq .Values.cluster.redundancyMode "three_datacenter") (eq .Values.cluster.redundancyMode "three_datacenter_fallback") (eq .Values.cluster.redundancyMode "three_data_hall_fallback") }}
{{- print (max 1 (sub $replicas 2)) -}}
{{- else if eq .Values.cluster.redundancyMode "double" }}
{{- print (max 1 (sub $replicas 1)) -}}
{{- else }}
{{- print $replicas -}}
{{- end -}}
{{- end -}}