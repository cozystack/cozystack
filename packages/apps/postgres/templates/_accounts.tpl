{{/*
  Account Secret naming, shared with the Cozystack API: the API writes one
  Secret per tenant account at <release>.<account>.account, where <account>
  is the user name lowercased with every character outside [a-z0-9-] replaced
  by "-" (pkg/credentials/account.MapName), and the Cluster's managed roles
  point at it by that name. Two users whose names map to one account are
  refused by the API before the write, and here for a HelmRelease written
  around it, because CNPG would otherwise read one Secret for both roles.
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
