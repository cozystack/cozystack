{{/*
  Account naming, which must match pkg/credentials/account.MapName: the
  credential of a user is <release>.<account>, where <account> is the user
  name lowercased with every character outside [a-z0-9-] replaced by "-".
*/}}
{{- define "bucket.accountName" -}}
{{- regexReplaceAll "[^a-z0-9-]" (lower .) "-" -}}
{{- end -}}

{{/*
  The BucketAccess of a tenant user, which also names the Secret the COSI
  sidecar writes for it: <release>-<user>, as the chart named it before the
  conversion, so that platform migration 71 hands the existing one, and its
  keys, to the template; or <release>-<account> when <release>-<user> is not
  a valid object name, since no BucketAccess was ever created under it.
*/}}
{{- define "bucket.accessName" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- $legacy := printf "%s-%s" $root.Release.Name $user -}}
{{- if and (le (len $legacy) 253) (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$" $legacy) -}}
{{- $legacy -}}
{{- else -}}
{{- printf "%s-%s" $root.Release.Name (include "bucket.accountName" $user) -}}
{{- end -}}
{{- end -}}

{{/*
  The BucketAccessClass a user's grant comes from: the storage pool's, and
  its -readonly variant for a read-only user.
*/}}
{{- define "bucket.accessClass" -}}
{{- $root := index . 0 -}}
{{- $user := index . 1 -}}
{{- $root.Values._namespace.seaweedfs }}{{- if $root.Values.storagePool }}-{{ $root.Values.storagePool }}{{- end }}{{- if $user.readonly }}-readonly{{- end }}
{{- end -}}

{{- define "bucket.accountsGuard" -}}
{{- $seen := dict -}}
{{- range $user, $u := .Values.users -}}
  {{- if not (default false $u.platform) -}}
  {{- $mapped := include "bucket.accountName" $user -}}
  {{- if or (gt (len $mapped) 63) (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $mapped)) -}}
    {{- fail (printf "user %q maps to the account name %q, which must be at most 63 characters and start and end with a letter or a digit; rename the user" $user $mapped) -}}
  {{- end -}}
  {{- with index $seen $mapped -}}
    {{- fail (printf "users %q and %q both map to the account name %q; rename one of them" . $user $mapped) -}}
  {{- end -}}
  {{- $_ := set $seen $mapped $user -}}
  {{- end -}}
{{- end -}}
{{- end -}}
