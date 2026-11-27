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
  Cluster, so both must resolve the same image, and the Job compares its own
  PostgreSQL build with the server's to tell when a new image has rolled out.

  standard-trixie, not the bare tag: a bare tag is the system variant on
  Debian bullseye, which upstream stopped building. The standard variant,
  because backups go through the barman-cloud plugin and need none of the
  barman binaries the system variant adds. trixie, because the postgis
  flavor is on it already, so both image families share one Debian release.
*/}}
{{- define "postgres.imageName" -}}
{{- include "postgres.heldImageName" . | default (include "postgres.targetImageName" .) -}}
{{- end -}}

{{- define "postgres.targetImageName" -}}
{{- if eq (default "postgresql" .Values.flavor) "postgis" -}}
ghcr.io/cloudnative-pg/postgis:{{ include "postgres.postgisVersionMap" . | trim }}
{{- else -}}
ghcr.io/cloudnative-pg/postgresql:{{ include "postgres.versionMap" . | trim | trimPrefix "v" }}-standard-trixie
{{- end -}}
{{- end -}}

{{/*
  The image a cluster on Debian bullseye keeps, or nothing. Clusters created
  before the move to trixie run one of the bare tags postgres.bullseyeImage
  lists, and moving them changes glibc and ICU under an initialised data
  directory. Such a cluster stays on its image until the pre-upgrade locale
  check has recorded "clean" on its Cluster, or until allowCollationChange is
  set: a cluster not checked yet, one the check could not finish on and one
  it found affected all stay. New majors are built only on the new images, so
  a held cluster refuses a change of version instead of dropping it.
*/}}
{{- define "postgres.heldImageName" -}}
{{- if and (ne (default "postgresql" .Values.flavor) "postgis") (not .Values.allowCollationChange) -}}
{{- $hold := include "postgres.localeHold" . | fromJson -}}
{{- if and $hold.image (ne $hold.verdict "clean") -}}
{{- $heldMajor := regexFind "^[0-9]+" (last (splitList ":" $hold.image)) -}}
{{- $wantMajor := trimPrefix "v" .Values.version -}}
{{- if ne $heldMajor $wantMajor -}}
{{- fail (printf "postgres: version %s cannot be applied: the cluster stays on %s until the pre-upgrade check finds nothing in it that depends on a collation sorting differently on the images PostgreSQL %s is built on (the postgres.cozystack.io/locale-data annotation of the cnpg.io Cluster). Keep version v%s, or set allowCollationChange: true and rebuild the affected indexes in a maintenance window." .Values.version $hold.image $wantMajor $heldMajor) -}}
{{- end -}}
{{- $hold.image -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
  The bullseye image that applies to this cluster and its check result, as
  JSON; the image is empty when the cluster runs any other one. The result is
  the postgres.cozystack.io/locale-data annotation the pre-upgrade check
  writes on the live Cluster. A physical recovery with no live Cluster yet
  (the CNPG backup driver deletes it before the recovery renders) restores
  onto the image the backup was taken on, so that the data keeps the library
  that built it: bootstrap.sourceImageName, which the driver copies from the
  source Cluster, or for a recovery set up by hand the bootstrap.oldName
  Cluster while it exists. A recovery has no result of its own and is held
  until the first upgrade after it checks the restored data.

  bootstrap.sourceImageName is in the application spec, so anyone who may
  edit the application may set it; it only ever selects one of the images
  postgres.bullseyeImage lists.
*/}}
{{- define "postgres.localeHold" -}}
{{- $live := lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Release.Name -}}
{{- $image := dig "spec" "imageName" "" $live -}}
{{- $verdict := dig "metadata" "annotations" "postgres.cozystack.io/locale-data" "" $live -}}
{{- if and .Values.bootstrap.enabled (not $image) -}}
{{- $image = .Values.bootstrap.sourceImageName | default (dig "spec" "imageName" "" (include "postgres.recoverySource" . | fromJson)) -}}
{{- $verdict = "" -}}
{{- end -}}
{{- if ne (include "postgres.bullseyeImage" $image) "true" -}}
{{- $image = "" -}}
{{- end -}}
{{- dict "image" $image "verdict" $verdict | toJson -}}
{{- end -}}

{{- define "postgres.recoverySource" -}}
{{- if and .Values.bootstrap.enabled .Values.bootstrap.oldName (not .Values.bootstrap.newServerName) -}}
{{- lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Values.bootstrap.oldName | toJson -}}
{{- else -}}
{}
{{- end -}}
{{- end -}}

{{/*
  The images this chart ran clusters on before the move to trixie: the bare
  version tags of the CloudNativePG image, which are its system variant on
  Debian bullseye, as files/versions.yaml listed them in every release before
  standard-trixie. Exact on purpose: a held image reaches spec.imageName.
*/}}
{{- define "postgres.bullseyeImage" -}}
{{- has . (list "ghcr.io/cloudnative-pg/postgresql:13.22" "ghcr.io/cloudnative-pg/postgresql:14.20" "ghcr.io/cloudnative-pg/postgresql:15.15" "ghcr.io/cloudnative-pg/postgresql:16.11" "ghcr.io/cloudnative-pg/postgresql:17.7" "ghcr.io/cloudnative-pg/postgresql:18.1") -}}
{{- end -}}

{{/*
  The live image of a cluster still on bullseye whose owner has not allowed
  the collation change, or nothing. The pre-upgrade locale check runs only
  then, from that image, against the server it is checking.
*/}}
{{- define "postgres.localeCheckImage" -}}
{{- if and (ne (default "postgresql" .Values.flavor) "postgis") (not .Values.allowCollationChange) (not .Values.bootstrap.enabled) -}}
{{- $live := lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Release.Name -}}
{{- if and $live $live.spec $live.spec.imageName (eq (include "postgres.bullseyeImage" $live.spec.imageName) "true") -}}
{{- $live.spec.imageName -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
  The flavor is fixed at bootstrap. Only the postgis image carries the shared
  libraries behind PostGIS objects, so the postgis-to-postgresql direction
  leaves every one of them without its code. The two families are also built
  and released separately, and nothing keeps their Debian release in step, so
  either direction can swap glibc and ICU under an initialised data directory
  together with the flavor. CNPG would roll either change out as an ordinary
  image update, so refuse it here, before the Cluster is patched. Only the
  image name is compared: a minor bump, a move to another variant of the same
  family, or a registry mirror rewriting the host must keep passing.

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
{{- fail (printf "postgres: flavor cannot change on an existing cluster (running %s, requested %s). Only the postgis image carries the PostGIS libraries, and the two image families can differ in glibc and ICU, which corrupts text indexes. Create a new Postgres with the wanted flavor and move the data with a logical dump (pg_dump / pg_restore); restoring a backup keeps the flavor it was taken from." $liveName $wantName) -}}
{{- end -}}
{{- else if and .Values.bootstrap.enabled .Values.bootstrap.oldName -}}
{{- $source := lookup "postgresql.cnpg.io/v1" "Cluster" .Release.Namespace .Values.bootstrap.oldName -}}
{{- if and $source $source.spec $source.spec.imageName -}}
{{- $sourceName := regexReplaceAll "[:@].*$" (last (splitList "/" $source.spec.imageName)) "" -}}
{{- if ne $sourceName $wantName -}}
{{- fail (printf "postgres: cannot recover a %s backup of %s into the %s flavor. A physical restore carries over a data directory built with the source's libraries: PostGIS objects need the postgis image, and a glibc or ICU difference corrupts text indexes. Set flavor to match the source, or create the new Postgres empty and move the data with a logical dump (pg_dump / pg_restore)." $sourceName .Values.bootstrap.oldName $wantName) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
