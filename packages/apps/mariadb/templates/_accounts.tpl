{{/*
  Account Secret naming, which must match pkg/credentials/account.MapName: the
  API writes <release>.<account>.account, where <account> is the user name
  lowercased with every character outside [a-z0-9-] replaced by "-".
*/}}
{{- define "mariadb.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{- define "mariadb.accountSecret" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- printf "%s.%s.account" $root.Release.Name (include "mariadb.accountName" $user) -}}
{{- end -}}

{{/*
  Tenant accounts: every key of users except root, which is the platform's
  own account in <release>-credentials and which user.yaml has always
  skipped.
*/}}
{{- define "mariadb.tenantUsers" -}}
{{- $users := dict -}}
{{- range $user, $u := .Values.users -}}
  {{- if ne $user "root" -}}
    {{- $_ := set $users $user $u -}}
  {{- end -}}
{{- end -}}
{{- toJson $users -}}
{{- end -}}

{{/*
  Two users that map to one account would make both User resources read the
  same account Secret, so a mint for one would change the other's password.
  The API refuses such a pair; this covers a HelmRelease written around it.
  Unlike the "_"/"-" User-name collision in secret.yaml, it fails on upgrade
  too: a release carrying such a pair keeps its previous revision, and the
  conversion leaves it out until one user is renamed.
*/}}
{{- define "mariadb.accountsGuard" -}}
{{- $seen := dict -}}
{{- range $user, $_ := (include "mariadb.tenantUsers" $ | fromJson) -}}
  {{- $mapped := include "mariadb.accountName" $user -}}
  {{- if or (gt (len $mapped) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped)) -}}
    {{- fail (printf "user %q maps to the account name %q, which must be at most 63 characters and start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
{{- end -}}
{{- end -}}
