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
{{- $derived := dict -}}
{{- range $database, $d := .Values.databases -}}
  {{- $adminRole := printf "%s_admin" $database -}}
  {{- if or (hasPrefix "pg_" $adminRole) (hasPrefix "cnpg_" $adminRole) -}}
    {{- fail (printf "database %q is reserved: its roles %s_admin and %s_readonly would start with pg_ or cnpg_, which PostgreSQL and CloudNativePG reserve and the CloudNativePG webhook rejects; rename the database" $database $database $database) -}}
  {{- end -}}
  {{- $_ := set $derived $adminRole $database -}}
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
  {{- /*
    A role a CREATEROLE user creates makes that user a member of it, which
    CloudNativePG revokes from a managed role and fails to, on the dependent
    self-grant, leaving the user stuck in cannotReconcile. CloudNativePG has
    no per-role switch to leave memberships alone, so the attribute is
    refused while users are managed roles.
  */ -}}
  {{- if default false $u.createRole -}}
    {{- fail (printf "users.%s.createRole is not supported: users are CloudNativePG managed roles, which revoke the membership PostgreSQL gives a CREATEROLE user in each role it creates. Set createRole to false; an existing release with it keeps its previous chart version until then" $user) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
