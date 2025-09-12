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