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

{{/* cluster.version is no longer read, but a release that still carries one
     runs that version today. Migration 59 carries it over to version where
     the platform runs migrations; nothing does on a variant that runs none,
     and there a new render would move the cluster to another release line
     without a word: down from 7.4, or off 7.1, which the operator no longer
     manages. Refusing the render keeps such a release on the revision it runs
     until someone picks the line. A patch-level difference inside the same
     line is left alone. */}}
{{- define "foundationdb.checkLegacyVersion" }}
{{- $cluster := .Values.cluster }}
{{- if kindIs "map" $cluster }}
{{-   $legacy := get $cluster "version" }}
{{-   if and (not (kindIs "invalid" $legacy)) (ne (toString $legacy) "") }}
{{-     $line := regexFind "^[0-9]+\\.[0-9]+" (toString $legacy) }}
{{-     $version := .Values.version | toString }}
{{-     if ne $line (trimPrefix "v" $version) }}
{{-       printf `cluster.version %s is no longer read, and it is on a different release line than version %s. Set version to the line the cluster runs, upgrading the cluster first if that line is no longer offered, or remove cluster.version to move the cluster to %s.` (toString $legacy) $version (include "foundationdb.versionMap" .) | fail }}
{{-     end }}
{{-   end }}
{{- end }}
{{- end }}
