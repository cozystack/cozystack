{{- define "hami.dra.webhook.fullname" -}}
{{- printf "%s-%s" (include "common.names.fullname" .) "webhook" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "hami.dra.webhook.image" -}}
{{ include "common.images.image" (dict "imageRoot" .Values.webhook.image "global" .Values.global) }}
{{- end -}}

{{- define "hami.dra.webhook.imagePullSecrets" -}}
{{ include "common.images.pullSecrets" (dict "images" (list .Values.webhook.image) "global" .Values.global) }}
{{- end -}}

{{- define "hami.dra.driver.nvidia.image" -}}
{{ include "common.images.image" (dict "imageRoot" .Values.drivers.nvidia.image "global" .Values.global) }}
{{- end -}}

{{- define "hami.dra.driver.nvidia.imagePullSecrets" -}}
{{ include "common.images.pullSecrets" (dict "images" (list .Values.drivers.nvidia.image) "global" .Values.global) }}
{{- end -}}

{{/*
Common labels
*/}}
{{- define "hami-dra-webhook.labels" -}}
helm.sh/chart: {{ .Chart.Name }}
{{ include "hami-dra-webhook.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "hami-dra-webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ .Release.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: webhook
{{- end }}

{{- define "hami.dra.monitor.fullname" -}}
{{- printf "%s-%s" (include "common.names.fullname" .) "monitor" | trunc 63 | trimSuffix "-" -}}
{{- end }}

{{- define "hami.dra.monitor.image" -}}
{{ include "common.images.image" (dict "imageRoot" .Values.monitor.image "global" .Values.global) }}
{{- end -}}

{{- define "hami.dra.monitor.imagePullSecrets" -}}
{{ include "common.images.pullSecrets" (dict "images" (list .Values.monitor.image) "global" .Values.global) }}
{{- end -}}

{{/*
Common labels for monitor
*/}}
{{- define "hami-dra-monitor.labels" -}}
helm.sh/chart: {{ .Chart.Name }}
{{ include "hami-dra-monitor.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels for monitor
*/}}
{{- define "hami-dra-monitor.selectorLabels" -}}
app.kubernetes.io/name: {{ .Release.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: monitor
{{- end }}
