{{/*
  Account Secret naming, which must match pkg/credentials/account.MapName: the
  credentials API writes <release>.<account>.account, where <account> is the
  user name lowercased with every character outside [a-z0-9-] replaced by "-".
*/}}
{{- define "rabbitmq.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{- define "rabbitmq.accountSecret" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- printf "%s.%s.account" $root.Release.Name (include "rabbitmq.accountName" $user) -}}
{{- end -}}

{{/*
  Two users mapping to one account would share one Secret, so the topology
  operator would import the same credentials for both User objects.
*/}}
{{- define "rabbitmq.accountsGuard" -}}
{{- $seen := dict -}}
{{- range $user, $_ := .Values.users -}}
  {{- $mapped := include "rabbitmq.accountName" $user -}}
  {{- if or (gt (len $mapped) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped)) -}}
    {{- fail (printf "user %q maps to the account name %q, which must be at most 63 characters and start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
{{- end -}}
{{- end -}}
