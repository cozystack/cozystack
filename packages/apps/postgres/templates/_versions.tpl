{{- define "postgres.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `PostgreSQL version %s is not supported, allowed versions are %s` $.Values.version (keys $versionMap) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- define "postgres.postgisVersionMap" }}
{{- $versionMap := .Files.Get "files/postgis-versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `PostgreSQL version %s is not supported by the postgis flavor, allowed versions are %s` $.Values.version (keys $versionMap | sortAlpha) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{- define "postgres.imageName" -}}
{{- $flavor := default "postgresql" .Values.flavor -}}
{{- if eq $flavor "postgis" -}}
{{- printf "ghcr.io/cloudnative-pg/postgis:%s" (include "postgres.postgisVersionMap" . | trim) -}}
{{- else -}}
{{- printf "ghcr.io/cloudnative-pg/postgresql:%s" (include "postgres.versionMap" . | trim | trimPrefix "v") -}}
{{- end -}}
{{- end -}}

