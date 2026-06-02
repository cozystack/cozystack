{{/*
redis.tls.enabled resolves the tri-state tls.enabled field to a plain
string "true" or "false" so callers can use it with `eq`:

  {{- $tlsEnabled := (include "redis.tls.enabled" .) | eq "true" -}}

Tri-state semantics:
  - tls.enabled explicitly set (bool) → use that value
  - tls.enabled unset or null         → auto-on when external is true
  - tls: null                         → treated as unset, falls back to external

`default (dict)` guards against tls: null (nil map). `kindIs "invalid"`
catches the case where the tls.enabled key is present but set to null
(hasKey returns true but index gives nil, and nil | toString = "<nil>"
which silently breaks the tri-state). Null value is treated the same as
unset: fall back to external.
*/}}
{{- define "redis.tls.enabled" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- $enabled := index $tlsMap "enabled" -}}
{{- if kindIs "invalid" $enabled -}}
  {{- .Values.external | default false | toString -}}
{{- else -}}
  {{- $enabled | toString -}}
{{- end -}}
{{- end -}}

{{/*
redis.tls.authClients resolves the optional tls.authClients field.
Returns "no" when unset to match the redis-operator default.
*/}}
{{- define "redis.tls.authClients" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- $authClients := index $tlsMap "authClients" -}}
{{- if kindIs "invalid" $authClients -}}
no
{{- else -}}
{{- $authClients -}}
{{- end -}}
{{- end -}}
