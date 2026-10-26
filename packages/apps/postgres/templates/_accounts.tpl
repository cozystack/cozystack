{{/*
  Account Secret naming, which must match pkg/credentials/account.MapName: the
  API writes <release>.<account>.account, where <account> is the user name
  lowercased with every character outside [a-z0-9-] replaced by "-". The API
  refuses two users that map to one account; the guard here covers a
  HelmRelease written around it, where CNPG would read one Secret for both.
*/}}
{{- define "postgres.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{- define "postgres.accountSecret" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- printf "%s.%s.account" $root.Release.Name (include "postgres.accountName" $user) -}}
{{- end -}}

{{- define "postgres.accountsGuard" -}}
{{- $seen := dict -}}
{{- range $user, $u := .Values.users -}}
  {{- $mapped := include "postgres.accountName" $user -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
{{- end -}}
{{- end -}}
