{{/* toString keeps an explicit `version: null` in the release values on the
     unsupported-version message below. Helm drops such a key when it merges
     the chart defaults, so indexing the map with it would fail on a template
     type error naming neither the value nor the versions on offer. */}}
{{- define "foundationdb.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- $version := .Values.version | toString }}
{{- if not (hasKey $versionMap $version) }}
    {{- printf `FoundationDB version %s is not supported, allowed versions are %s` $version (keys $versionMap | sortAlpha) | fail }}
{{- end }}
{{- index $versionMap $version }}
{{- end }}
