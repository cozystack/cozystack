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
it on — in particular `external` does not, unlike the sibling nats and qdrant
charts. See README "TLS scope" for why.

Every template that branches on TLS calls this helper, so they cannot disagree
about whether TLS is on; tests/tls_resolution_test.yaml pins that agreement.

The `dig` on a defaulted dict is load-bearing: reading .Values.tls.enabled
directly panics with "nil pointer evaluating interface {}.enabled" on `tls: null`,
which values.schema.json accepts and which reaches the chart unpruned through a
HelmRelease override. The kindIs check then covers the ordinary path, where
`tls: {}` from values.yaml makes dig return its nil default.
*/}}
{{- define "rabbitmq.tls.enabled" -}}
{{-   $enabled := dig "enabled" nil (.Values.tls | default dict) -}}
{{-   if kindIs "invalid" $enabled -}}
{{-     "false" -}}
{{-   else -}}
{{-     $enabled | toString -}}
{{-   end -}}
{{- end -}}
