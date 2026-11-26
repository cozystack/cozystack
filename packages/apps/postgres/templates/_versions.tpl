{{- define "postgres.versionMap" }}
{{- $versionMap := .Files.Get "files/versions.yaml" | fromYaml }}
{{- if not (hasKey $versionMap .Values.version) }}
    {{- printf `PostgreSQL version %s is not supported, allowed versions are %s` $.Values.version (keys $versionMap) | fail }}
{{- end }}
{{- index $versionMap .Values.version }}
{{- end }}

{{/*
  Single source for the operand image: the init Job runs psql against the
  Cluster, so both must resolve the same image, and the Job compares its own
  PostgreSQL build with the server's to tell when a new image has rolled out.

  standard-trixie, not the bare tag: a bare tag is the system variant on
  Debian bullseye, which upstream stopped building. The standard variant,
  because backups go through the barman-cloud plugin and need none of the
  barman binaries the system variant adds.
*/}}
{{- define "postgres.imageName" -}}
ghcr.io/cloudnative-pg/postgresql:{{ include "postgres.versionMap" . | trim | trimPrefix "v" }}-standard-trixie
{{- end -}}
