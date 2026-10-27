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

{{/*
  Refusals for names the managed roles would otherwise hand to CNPG. They
  render with the Cluster on every path, recovery included, where the
  init-script does not render.
*/}}
{{- define "postgres.accountsGuard" -}}
{{- $pgMajor := atoi (trimPrefix "v" .Values.version) -}}
{{- $derived := dict -}}
{{- range $database, $d := .Values.databases -}}
  {{- $_ := set $derived (printf "%s_admin" $database) $database -}}
  {{- $_ := set $derived (printf "%s_readonly" $database) $database -}}
{{- end -}}
{{- $seen := dict -}}
{{- range $user, $u := .Values.users -}}
  {{- $mapped := include "postgres.accountName" $user -}}
  {{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped) -}}
    {{- fail (printf "user %q maps to the account name %q, which must start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
  {{- if or (eq $user "streaming_replica") (hasPrefix "pg_" $user) (hasPrefix "cnpg_" $user) -}}
    {{- fail (printf "user %q is reserved: streaming_replica and names starting with pg_ or cnpg_ belong to PostgreSQL and CloudNativePG; rename the user" $user) -}}
  {{- end -}}
  {{- with index $derived $user -}}
    {{- fail (printf "user %q is the name of a role of database %q; rename the user" $user .) -}}
  {{- end -}}
  {{- if and (default false $u.createRole) (lt $pgMajor 16) -}}
    {{- fail (printf "SECURITY: users.%s.createRole requires PostgreSQL 16 or newer. On PostgreSQL 15 and older, a role holding CREATEROLE can grant itself any non-superuser role, including pg_execute_server_program, which escalates to command execution on the instance." $user) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
