{{- /*
  Bucket and prefix of backup.destinationPath. The bootstrap restore has to
  read from the place the backup storage writes to. The operator builds the
  restore's storage from backupSource.s3 alone and takes only the backup name
  from backupSource.destination, so both templates derive the pair here.
*/}}
{{- define "mongodb.backup.bucket" -}}
{{- .Values.backup.destinationPath | trimPrefix "s3://" | regexFind "^[^/]+" -}}
{{- end -}}

{{- define "mongodb.backup.prefix" -}}
{{- .Values.backup.destinationPath | trimPrefix "s3://" | splitList "/" | rest | join "/" -}}
{{- end -}}
