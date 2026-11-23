{{/*
  Account Secret naming, which must match pkg/credentials/account.MapName: the
  API writes <release>.<account>.account, where <account> is the user name
  lowercased with every character outside [a-z0-9-] replaced by "-". The API
  refuses two users that map to one account; the guard here covers a
  HelmRelease written around it, where the operator would read one Secret for
  both.
*/}}
{{- define "mongodb.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{- define "mongodb.accountSecret" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- printf "%s.%s.account" $root.Release.Name (include "mongodb.accountName" $user) -}}
{{- end -}}

{{/*
  Refusals for user names the operator would not apply. The system users are
  platform accounts kept in <release>-percona-server-mongodb-users; the
  operator skips a custom user that reuses one of their names, so a template
  for it would mint a password that never reaches the engine.
*/}}
{{- define "mongodb.accountsGuard" -}}
{{- $reserved := include "mongodb.systemUserNames" . | fromJsonArray -}}
{{- $seen := dict -}}
{{- range $user, $_ := .Values.users -}}
  {{- $mapped := include "mongodb.accountName" $user -}}
  {{- if or (gt (len $mapped) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped)) -}}
    {{- fail (printf "user %q maps to the account name %q, which must be at most 63 characters and start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
  {{- if has $user $reserved -}}
    {{- fail (printf "user %q is reserved: it is the name of a MongoDB system user the platform manages; rename the user" $user) -}}
  {{- end -}}
{{- end -}}
{{- end -}}

{{/*
  The role every user the chart declares holds, and nothing else does: only
  the operator, through spec.users, grants it.
*/}}
{{- define "mongodb.managedRole" -}}
cozystack-managed
{{- end -}}

{{- define "mongodb.systemUserNames" -}}
{{- list "backup" "clusterAdmin" "clusterMonitor" "databaseAdmin" "userAdmin" | toJson -}}
{{- end -}}
