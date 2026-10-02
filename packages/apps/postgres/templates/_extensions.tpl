{{/*
  The name CREATE EXTENSION resolves for a databases.<db>.extensions entry, before
  it is quoted. Charts that rendered the value unquoted made a hyphenated extension
  reachable only by writing its quotes into the value ('"uuid-ossp"'), so one
  surrounding pair is the identifier's own quoting: it is removed and, as a quoted
  identifier, keeps its case. An unquoted name is folded to lowercase, the way
  PostgreSQL resolved it unquoted (PostGIS to postgis). The charset guard in
  init-script.yaml checks this result, so a quote inside the pair still fails it.
*/}}
{{- define "postgres.extensionName" -}}
{{- if regexMatch "^\".+\"$" . -}}
{{- trimSuffix "\"" (trimPrefix "\"" .) -}}
{{- else -}}
{{- lower . -}}
{{- end -}}
{{- end -}}
