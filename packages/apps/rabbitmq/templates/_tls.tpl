{{/*
rabbitmq.tls.enabled resolves the tri-state tls.enabled field to the string
"true" or "false" so callers can compare with `eq`:

  {{- $tlsEnabled := (include "rabbitmq.tls.enabled" .) | eq "true" -}}

Tri-state semantics:
  - tls.enabled explicitly set   → use that value
  - tls.enabled unset or null    → inherit from external

Every template that branches on TLS calls this helper, so they cannot disagree
about whether TLS is on. tests/tls_tristate_test.yaml pins that agreement.

The lookup goes through `dig` on a defaulted dict rather than reading
.Values.tls.enabled directly, so `tls: null` resolves instead of panicking with
"nil pointer evaluating interface {}.enabled". The app CR cannot produce that
shape — structural-schema pruning replaces null with the {} default — but a
HelmRelease values override reaches the chart without pruning, and null passes
values.schema.json.

Non-map scalars need no handling here: values.schema.json types tls as an object,
so `tls: false` or `tls: "x"` is rejected by Helm's own schema validation ("got
boolean, want object") before any template renders, on every path including a
HelmRelease. Only null slips through, which is what the dig form covers.
*/}}
{{- define "rabbitmq.tls.enabled" -}}
{{-   $enabled := dig "enabled" nil (.Values.tls | default dict) -}}
{{-   if kindIs "invalid" $enabled -}}
{{-     .Values.external | default false | toString -}}
{{-   else -}}
{{-     $enabled | toString -}}
{{-   end -}}
{{- end -}}
