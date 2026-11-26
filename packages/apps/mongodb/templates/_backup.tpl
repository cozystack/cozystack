{{- /*
  Bucket and prefix of an s3://bucket/prefix path. The operator builds a
  restore's storage from backupSource.s3 alone and takes only the backup name
  from backupSource.destination, so the restore needs the pair of the path it
  reads from, just as the backup storage needs the pair of the path it writes to.
*/}}
{{- define "mongodb.s3.bucket" -}}
{{- . | trimPrefix "s3://" | regexFind "^[^/]+" -}}
{{- end -}}

{{- define "mongodb.s3.prefix" -}}
{{- . | trimPrefix "s3://" | splitList "/" | rest | join "/" -}}
{{- end -}}

{{- define "mongodb.backup.bucket" -}}
{{- include "mongodb.s3.bucket" .Values.backup.destinationPath -}}
{{- end -}}

{{- define "mongodb.backup.prefix" -}}
{{- include "mongodb.s3.prefix" .Values.backup.destinationPath -}}
{{- end -}}

{{- define "mongodb.bootstrap.path" -}}
{{- .Values.bootstrap.destinationPath | default .Values.backup.destinationPath -}}
{{- end -}}
