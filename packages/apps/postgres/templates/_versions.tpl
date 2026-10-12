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

{{/*
  Single source for the operand image: the init Job runs psql against the
  Cluster, so both must resolve the same image.
*/}}
{{- define "postgres.imageName" -}}
{{- if eq (default "postgresql" .Values.flavor) "postgis" -}}
ghcr.io/cloudnative-pg/postgis:{{ include "postgres.postgisVersionMap" . | trim }}
{{- else -}}
ghcr.io/cloudnative-pg/postgresql:{{ include "postgres.versionMap" . | trim | trimPrefix "v" }}
{{- end -}}
{{- end -}}

{{/*
  The flavor is fixed at bootstrap. The two image families are built on
  different Debian releases, so moving a live cluster from one to the other
  swaps glibc under an initialised data directory and silently invalidates
  every index on collatable text; the postgis-to-postgresql direction also
  drops the shared libraries behind every PostGIS object. CNPG would roll
  either change out as an ordinary image update, so refuse it here, before
  the Cluster is patched. Only the image name is compared: a minor bump or
  a registry mirror rewriting the host must keep passing.

  A physical recovery carries the same data directory across, so it is held
  to the same rule. With no live Cluster of its own yet, the release is
  checked against its recovery source while that Cluster still exists; once
  the source is gone the chart has nothing to compare against, and the
  backup-controller's RestoreJob, which records the flavor on every Backup,
  is the path that enforces it.
*/}}
{{- define "postgres.flavorGuard" -}}
{{- $wantName := regexReplaceAll "[:@].*$" (last (splitList "/" (include "postgres.imageName" .))) "" -}}
{{- $live := lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Release.Name -}}
{{- if and $live $live.spec $live.spec.imageName -}}
{{- $liveName := regexReplaceAll "[:@].*$" (last (splitList "/" $live.spec.imageName)) "" -}}
{{- if ne $liveName $wantName -}}
{{- fail (printf "postgres: flavor cannot change on an existing cluster (running %s, requested %s). The image families use different glibc versions, which would corrupt text indexes. Create a new Postgres with the wanted flavor and move the data with a logical dump (pg_dump / pg_restore); restoring a backup keeps the flavor it was taken from." $liveName $wantName) -}}
{{- end -}}
{{- else if and .Values.bootstrap.enabled .Values.bootstrap.oldName -}}
{{- $source := lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Values.bootstrap.oldName -}}
{{- if and $source $source.spec $source.spec.imageName -}}
{{- $sourceName := regexReplaceAll "[:@].*$" (last (splitList "/" $source.spec.imageName)) "" -}}
{{- if ne $sourceName $wantName -}}
{{- fail (printf "postgres: cannot recover a %s backup of %s into the %s flavor. A physical restore keeps the source's glibc and libraries, which would corrupt text indexes. Set flavor to match the source, or create the new Postgres empty and move the data with a logical dump (pg_dump / pg_restore)." $sourceName .Values.bootstrap.oldName $wantName) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
