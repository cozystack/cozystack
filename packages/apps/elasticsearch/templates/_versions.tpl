{{/*
Elasticsearch version mapping
*/}}
{{- define "elasticsearch.versionMap" -}}
{{- $versions := .Files.Get "files/versions.yaml" | fromYaml -}}
{{- $version := .Values.version -}}
{{- if hasKey $versions $version -}}
{{- index $versions $version -}}
{{- else -}}
{{- fail (printf "Unsupported Elasticsearch version: %s. Supported versions: %s" $version (keys $versions | sortAlpha | join ", ")) -}}
{{- end -}}
{{- end -}}
