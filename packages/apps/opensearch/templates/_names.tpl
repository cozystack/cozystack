{{/*
opensearch.validateReleaseName fails the render when the release name is long enough
that some object this chart produces would be rejected by the API server, or would
carry a DNS label the certificate cannot legally contain.

Two different limits are in play, and conflating them is how this went wrong before:

  - Certificate, Issuer, Secret, Role and RoleBinding names are DNS-1123 SUBDOMAINS,
    bounded at 253. Nothing here approaches that, so they never bind.
  - Service names are DNS-1035 LABELS, bounded at 63. So is every label inside a
    certificate SAN. These are what actually bind.

Which one binds depends on the configuration, because the longest name is only built
in some of them. The suffixes, longest first:

  -dashboards-external  (20)  Service, when external and dashboards are both on
  -dashboards           (11)  operator-created Service and a SAN label, when dashboards is on
  -discovery            (10)  SAN label, when chart-managed TLS is on
  -external              (9)  Service, when external is on

The guard takes the longest suffix that the current values actually produce and caps
the release name at 63 minus its length. It is invoked from every template that
renders one of these names, including the ones that render with TLS off, because the
Service names have nothing to do with TLS.
*/}}
{{- define "opensearch.validateReleaseName" -}}
{{- $external := .Values.external | default false -}}
{{- $dashboards := .Values.dashboards.enabled | default false -}}
{{- $tlsEnabled := (include "opensearch.tls.enabled" .) | eq "true" -}}
{{- $suffix := "" -}}
{{- if and $external $dashboards -}}
  {{- $suffix = "-dashboards-external" -}}
{{- else if $dashboards -}}
  {{- $suffix = "-dashboards" -}}
{{- else if $tlsEnabled -}}
  {{- $suffix = "-discovery" -}}
{{- else if $external -}}
  {{- $suffix = "-external" -}}
{{- end -}}
{{- if $suffix -}}
  {{- $max := sub 63 (len $suffix) | int -}}
  {{- if gt (len .Release.Name) $max -}}
    {{- fail (printf "Release name %q is %d chars; opensearch requires <=%d in this configuration so that %q stays within the 63-char DNS label limit." .Release.Name (len .Release.Name) $max (printf "%s%s" .Release.Name $suffix)) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
