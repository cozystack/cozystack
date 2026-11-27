{{/*
  Account naming, which must match pkg/credentials/account.MapName: the
  template is <release>.<account> and its Secret <release>.<account>.account,
  where <account> is the user name lowercased with every character outside
  [a-z0-9-] replaced by "-".
*/}}
{{- define "clickhouse.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{/*
  The stored SHA-256 of a user's password, which the HelmRelease pulls from
  the account Secret into _accounts.u<sha256 of the user name> through the
  valuesFrom entry cozystack-controller adds for the template. Empty until the
  Secret exists, and for anything but a SHA-256 hex, so a value in another
  format leaves the account out rather than giving the engine a broken one.
*/}}
{{- define "clickhouse.accountVerifier" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- $v := get ($root.Values._accounts | default dict) (printf "u%s" (sha256sum $user)) | toString | trim | lower -}}
{{- if regexMatch "^[0-9a-f]{64}$" $v -}}
{{- $v -}}
{{- end -}}
{{- end -}}

{{/*
  Refusals for user names. backup is the platform account the backup jobs log
  in with, default is the account the replicas talk to each other with, and
  clickhouse_operator the one the operator logs in with: a tenant account
  under any of them would take over or expose a platform login.
*/}}
{{- define "clickhouse.accountsGuard" -}}
{{- $seen := dict -}}
{{- range $user, $_ := .Values.users -}}
  {{- if has $user (list "backup" "default" "clickhouse_operator") -}}
    {{- fail (printf "user %q is reserved: backup, default and clickhouse_operator are platform accounts; rename the user" $user) -}}
  {{- end -}}
  {{- $mapped := include "clickhouse.accountName" $user -}}
  {{- if or (gt (len $mapped) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped)) -}}
    {{- fail (printf "user %q maps to the account name %q, which must be at most 63 characters and start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
{{- end -}}
{{- end -}}
