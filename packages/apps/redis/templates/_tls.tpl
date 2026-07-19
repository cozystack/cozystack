{{/*
redis.tls.enabled resolves the tls.enabled field to a plain string "true"
or "false" so callers can use it with `eq`:

  {{- $tlsEnabled := (include "redis.tls.enabled" .) | eq "true" -}}

Semantics — TLS is opt-in and is never inferred:
  - tls.enabled explicitly set (bool) → use that value
  - tls.enabled unset                 → false
  - tls: null                         → treated as unset, so false

TLS deliberately does NOT follow `external`. In this operator TLS replaces
plaintext rather than running beside it — the rendered config sets `port 0`
and moves the listener to `tls-port` — so inferring it from `external` would
switch an already-running externally-reachable Redis to TLS-only on its
first reconcile after an upgrade and drop every plaintext client mid-flight.
A platform upgrade must not sever working connections, so enabling TLS stays
an explicit decision by whoever also migrates the clients.

`default (dict)` guards against tls: null (nil map). `kindIs "invalid"`
catches the case where tls is a map with no enabled key at all: index
returns nil, and nil | toString = "<nil>", which would read as neither
"true" nor "false".

An explicitly null enabled is not among the shapes reaching here —
values.schema.json types the field as boolean and rejects null before any
template runs — so the guard is about the absent key, not a null one.
*/}}
{{- define "redis.tls.enabled" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- $enabled := index $tlsMap "enabled" -}}
{{- if kindIs "invalid" $enabled -}}
  {{- "false" -}}
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
