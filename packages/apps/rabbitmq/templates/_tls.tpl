{{/*
rabbitmq.clusterDomain returns the cluster DNS domain, defaulting to cozy.local.
Used by the certificate SAN list and by the topology-operator connection URI, which
must agree on how a Service is named.
*/}}
{{- define "rabbitmq.clusterDomain" -}}
{{-   $domain := "cozy.local" -}}
{{-   if .Values._cluster -}}
{{-     $domain = (index .Values._cluster "cluster-domain") | default "cozy.local" -}}
{{-   end -}}
{{-   $domain -}}
{{- end -}}

{{/*
rabbitmq.tls.enabled resolves tls.enabled to the string "true" or "false" so
callers can compare with `eq`:

  {{- $tlsEnabled := (include "rabbitmq.tls.enabled" .) | eq "true" -}}

TLS is opt-in: unset, null and false all resolve to false, and nothing else turns
it on. In particular `external` does not — publishing a broker outside the cluster
and encrypting it are separate decisions, so enabling one never enables the other
behind the operator's back.

Every template that branches on TLS calls this helper, so they cannot disagree
about whether TLS is on. tests/tls_resolution_test.yaml pins that agreement.

The lookup goes through `dig` on a defaulted dict rather than reading
.Values.tls.enabled directly, so `tls: null` resolves instead of panicking with
"nil pointer evaluating interface {}.enabled". The app CR cannot produce that
shape — structural-schema pruning replaces null with the {} default — but a
HelmRelease values override reaches the chart without pruning, and null passes
values.schema.json.

The kindIs check then covers the missing key, which is the ordinary default path:
`tls: {}` ships in values.yaml, and dig returns its nil default for it.

Two shapes need no handling here, because values.schema.json rejects them before
any template renders, on every path including a HelmRelease: `tls: false` or
`tls: "x"` ("got boolean, want object"), and `tls: {enabled: null}` ("got null,
want boolean"). Only a null `tls` slips through, which is what the dig form
covers.
*/}}
{{- define "rabbitmq.tls.enabled" -}}
{{-   $enabled := dig "enabled" nil (.Values.tls | default dict) -}}
{{-   if kindIs "invalid" $enabled -}}
{{-     "false" -}}
{{-   else -}}
{{-     $enabled | toString -}}
{{-   end -}}
{{- end -}}
