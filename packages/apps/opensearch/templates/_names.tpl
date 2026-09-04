{{/*
opensearch.validateReleaseName fails the render when the release name is long enough
that some object THIS CHART renders would be rejected by the API server, or would
carry a DNS label the certificate cannot legally contain. Ownership is the whole of
it: a name the chart does not render is not a name a render failure can save.

Two different limits are in play, and conflating them is how this went wrong before:

  - Certificate, Issuer, Secret, Role and RoleBinding names are DNS-1123 SUBDOMAINS,
    bounded at 253. Nothing here approaches that, so they never bind.
  - Service names are DNS-1035 LABELS, bounded at 63. So is every label inside a
    certificate SAN. These are what actually bind.

Which one binds depends on the configuration, because the longest name is only built
in some of them. The suffixes, longest first:

  -dashboards-external  (20)  Service, when external and dashboards are both on
  -discovery            (10)  Service the operator always creates, and a SAN label when
                              chart-managed TLS is on. NewDiscoveryServiceForCR runs on
                              every path, so this name exists regardless of TLS — the
                              TLS branch below is about the SAN, not about the Service.
  -external              (9)  Service, when external is on

The guard takes the longest suffix that the current values actually produce and caps
the release name at 63 minus its length. It is invoked from every template that
renders one of these names, including the ones that render with TLS off, because the
Service names have nothing to do with TLS.

NOT GUARDED: -dashboards (11), which would cap the name at 52. The apps API admits
release names up to 53 (maxHelmReleaseName in pkg/registry/apps/application/rest.go),
so such a cap refuses a release a tenant can legitimately create, and the object it
refuses it for belongs to the operator. The operator composes the Dashboards Service
as <spec.general.serviceName>-dashboards (NewDashboardsSvcForCr) and creates it last,
through a combined result, after the ConfigMap and the Deployment — so at 53 characters
the cluster and the Dashboards pod still come up and that one Service is all that is
refused. Failing the render withholds every object instead, which leaves the
HelmRelease unable to converge and no later edit to any field of the application able
to land, with no way out of the state: the release name is fixed and validateNameLength
rejects a rename. The Dashboards SAN carries the same overflowing label, and that is
not a second bound — cert-manager does not validate DNS label length in dnsNames, so
nothing refuses the Certificate.

REACHABILITY: Helm itself rejects a release name over 53 characters
(chartutil.ValidateReleaseName), so -dashboards-external (43) is the only bound that
can ever fire. The -discovery (53) and -external (54) bounds are unreachable through
Helm and kept as a backstop only: they are what makes the table above complete, and
the arithmetic stays correct if the suffixes change. Do not read a passing render at
53 characters as those branches working.
*/}}
{{- define "opensearch.validateReleaseName" -}}
{{- $external := .Values.external | default false -}}
{{- $dashboards := .Values.dashboards.enabled | default false -}}
{{- $tlsEnabled := (include "opensearch.tls.enabled" .) | eq "true" -}}
{{- $suffix := "" -}}
{{- if and $external $dashboards -}}
  {{- $suffix = "-dashboards-external" -}}
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
