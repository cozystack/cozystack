{{/*
Version mapping helper
Loads version mapping from files/versions.yaml and returns the full version for a given major version
*/}}
{{- define "opensearch.versionMap" -}}
{{- $versions := .Files.Get "files/versions.yaml" | fromYaml -}}
{{- $version := .Values.version | default "v2" -}}
{{- if hasKey $versions $version -}}
{{- index $versions $version -}}
{{- else -}}
{{- fail (printf "Invalid version '%s'. Available versions: %s" $version (keys $versions | join ", ")) -}}
{{- end -}}
{{- end -}}
