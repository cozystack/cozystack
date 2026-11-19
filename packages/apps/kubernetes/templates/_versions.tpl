{{- define "kubernetes.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `Kubernetes version %s is not supported, allowed versions are %s` $.Values.version (keys $versionMap) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- define "kubernetes.konnectivityVersion" }}
{{- $konnVersionMap := .Files.Get "files/konnectivity-versions.yaml" | fromYaml }}
{{- if hasKey $konnVersionMap .Values.version }}
{{- index $konnVersionMap .Values.version }}
{{- end }}
{{- end }}
