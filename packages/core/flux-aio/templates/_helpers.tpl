{{- define "cozy.kubernetes_envs" }}
{{- $cozyDeployment := lookup "apps/v1" "Deployment" "cozy-system" "cozystack" }}
{{- $cozyContainers := dig "spec" "template" "spec" "containers" dict $cozyDeployment }}
{{- range $cozyContainers }}
{{- if eq .name "cozystack" }}
{{- range .env }}
{{- if has .name (list "KUBERNETES_SERVICE_HOST" "KUBERNETES_SERVICE_PORT") }}
- {{ toJson . }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
{{- end }}
