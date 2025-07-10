{{- define "kubernetes.versionMap" }}
{{- $versionMap := dict `v1.28` `v1.28.15` `v1.29` `v1.29.15` `v1.30` `v1.30.14` `v1.31` `v1.31.10` `v1.32` `v1.32.6` `v1.33` `v1.33.2` }}
{{- if not (hasKey $versionMap .) }}
{{- printf `Kubernetes version %s is not supported, allowed versions are %s` . (keys $versionMap) | fail }}
{{- end }}
{{- index $versionMap . }}
{{- end }}
