{{/*
mariadb.tls.enabled resolves tls.enabled to a plain string "true" or "false"
so callers can use it with `eq`:

  {{- $tlsEnabled := (include "mariadb.tls.enabled" .) | eq "true" -}}

"Enabled" here means the chart MANAGES TLS: it issues a per-instance CA and
server certificate. It does not mean TLS is on — the operator serves TLS in
every configuration — and turning it off does not turn TLS off.

Semantics:
  - tls.enabled explicitly set (bool) → use that value
  - tls.enabled unset or null         → false
  - tls: null                         → treated as unset → false

It deliberately does NOT derive from `external`. Deriving it would move an
existing external instance from the operator's CA to a chart-issued one on the
next platform upgrade, with no action from its owner, and any client pinning
the operator's ca.crt would fail chain validation. That trade would be
defensible if it raised the security floor, but it does not: those instances
already serve TLS under the operator's CA. It is a change of CA ownership for
the sake of the platform trust contract, so it has to be opted into.

`default (dict)` guards against tls: null (nil map). `kindIs "invalid"` catches
tls.enabled present but null (hasKey returns true while index gives nil, and
nil | toString is "<nil>", which would silently read as managed).
*/}}
{{- define "mariadb.tls.enabled" -}}
{{- $tlsMap := default (dict) .Values.tls -}}
{{- $enabled := index $tlsMap "enabled" -}}
{{- if kindIs "invalid" $enabled -}}
  {{- "false" -}}
{{- else -}}
  {{- $enabled | toString -}}
{{- end -}}
{{- end -}}
