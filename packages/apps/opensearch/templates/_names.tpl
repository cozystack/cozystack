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
  -dashboards           (11)  Service the operator creates when dashboards are on
  -discovery            (10)  Service the operator always creates, and a SAN label when
                              the cert-manager issuer is selected. NewDiscoveryServiceForCR runs on
                              every path, so this name exists regardless of TLS — the
                              TLS branch below is about the SAN, not about the Service.
  -external              (9)  Service, when external is on

The guard takes the longest suffix that the current values actually produce and caps
the release name at 63 minus its length. It is invoked from every template that
renders one of these names, including the ones that render under the operator
issuer, because the Service names have nothing to do with the issuer.

THE -dashboards BOUND COSTS THE LAST LEGAL NOTCH, deliberately. It caps the release
name at 52 while the apps API admits 53 (maxHelmReleaseName in
pkg/registry/apps/application/rest.go), so one length a tenant may legitimately ask for
is refused at render time. The alternative is worse at that same length. The operator
composes the Dashboards Service as <spec.general.serviceName>-dashboards
(NewDashboardsSvcForCr) and creates it last in DashboardsReconciler.Reconcile, whose
combined result carries the failure; controllers/opensearchController.go runs its
component reconcilers in one loop that returns on the first error, and
dashboards.Reconcile sits ahead of upgrade.Reconcile, restart.Reconcile and
snapshotrepository.Reconcile. A Service the API server refuses by length therefore does
not cost one object: it stops those three on every pass for the life of the release, so
the cluster comes up, converges, and then never upgrades, never rolls and never takes a
snapshot repository, with the reason only in the operator log. Both outcomes are bad at
53 characters. This one announces itself.

The Dashboards SAN carries the same overflowing label and is not a second bound:
cert-manager does not validate DNS label length in dnsNames, so nothing refuses the
Certificate.

REACHABILITY: Helm itself rejects a release name over 53 characters
(chartutil.ValidateReleaseName), so two of these bounds can fire, -dashboards-external
at 43 and -dashboards at 52. The -discovery (53) and -external (54) bounds are unreachable through
Helm and kept as a backstop only: they are what makes the table above complete, and
the arithmetic stays correct if the suffixes change. Do not read a passing render at
53 characters as those branches working.
*/}}
{{- define "opensearch.validateReleaseName" -}}
{{- $external := .Values.external | default false -}}
{{- $dashboards := .Values.dashboards.enabled | default false -}}
{{- $certManagerIssued := (include "opensearch.tls.certManagerIssued" .) | eq "true" -}}
{{- $suffix := "" -}}
{{- if and $external $dashboards -}}
  {{- $suffix = "-dashboards-external" -}}
{{- else if $dashboards -}}
  {{- $suffix = "-dashboards" -}}
{{- else if $certManagerIssued -}}
  {{- $suffix = "-discovery" -}}
{{- else if $external -}}
  {{- $suffix = "-external" -}}
{{- end -}}
{{- if $suffix -}}
  {{- $max := sub 63 (len $suffix) | int -}}
  {{- if gt (len .Release.Name) $max -}}
    {{- /*
    An application name cannot be changed in place, so "use a shorter name" is the
    one remedy an existing release does not have. Both reachable bounds come from
    the Dashboards Service, so name the knob that drops them.
    */ -}}
    {{- $escape := "" -}}
    {{- if $dashboards -}}
      {{- $escape = " Set dashboards.enabled to false to drop this bound." -}}
    {{- end -}}
    {{- fail (printf "Release name %q is %d chars; opensearch requires <=%d in this configuration so that %q stays within the 63-char DNS label limit.%s" .Release.Name (len .Release.Name) $max (printf "%s%s" .Release.Name $suffix) $escape) -}}
  {{- end -}}
{{- end -}}
{{- end -}}
