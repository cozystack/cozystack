{{/*
rabbitmq.tls.enabled resolves the tri-state tls.enabled field to the string
"true" or "false" so callers can compare with `eq`:

  {{- $tlsEnabled := (include "rabbitmq.tls.enabled" .) | eq "true" -}}

Tri-state semantics:
  - tls.enabled explicitly set   → use that value
  - tls.enabled unset or null    → inherit from external

Defined once because every template that branches on TLS must reach the same
answer. Three copies of the inline form previously existed in this chart and had
already begun to drift.

The lookup goes through `dig` on a defaulted dict rather than reading
.Values.tls.enabled directly, so `--set tls=null` resolves instead of panicking
with "nil pointer evaluating interface {}.enabled". The app CR cannot produce
that shape — structural-schema pruning replaces null with the {} default — but a
HelmRelease values override can.
*/}}
{{- define "rabbitmq.tls.enabled" -}}
{{-   $enabled := dig "enabled" nil (.Values.tls | default dict) -}}
{{-   if kindIs "invalid" $enabled -}}
{{-     .Values.external | default false | toString -}}
{{-   else -}}
{{-     $enabled | toString -}}
{{-   end -}}
{{- end -}}
