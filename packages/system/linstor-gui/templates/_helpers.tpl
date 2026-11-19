{{/*
Common labels
*/}}
{{- define "linstor-gui.labels" -}}
app.kubernetes.io/name: linstor-gui
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cozystack
{{- end }}

{{/*
Selector labels
*/}}
{{- define "linstor-gui.selectorLabels" -}}
app.kubernetes.io/name: linstor-gui
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
