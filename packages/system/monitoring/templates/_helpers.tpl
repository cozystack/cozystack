{{- /*
  Helpers for the Grafana OIDC integration.

  Naming — the identity unit is the Monitoring release, so the clientId
  and the associated groups are `<namespace>-<release>` prefixed. That
  gives:
    tenant-foo/monitoring        (per-tenant Grafana in tenant-foo ns)
    cozy-monitoring/monitoring-system  (platform Grafana in cozy-monitoring ns)
  A token minted for one instance is rejected by the other's Grafana
  because the per-cluster audience scope binds `id_token.aud` to the
  clientId — same isolation primitive as the tenant kube-apiserver PR
  (cozystack/cozystack#3044).
*/}}

{{- define "monitoring.oidc.clientId" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name -}}
{{- end -}}

{{- define "monitoring.oidc.audienceScopeName" -}}
{{- printf "%s-%s-audience" .Release.Namespace .Release.Name -}}
{{- end -}}

{{- define "monitoring.oidc.clientSecretName" -}}
{{- printf "%s-oidc-client" .Release.Name -}}
{{- end -}}

{{- define "monitoring.oidc.grafanaHost" -}}
{{- $namespaceHost := .Values._namespace.host -}}
{{- printf "grafana.%s" (.Values.host | default $namespaceHost) -}}
{{- end -}}

{{- define "monitoring.oidc.redirectUri" -}}
{{- printf "https://%s/login/generic_oauth" (include "monitoring.oidc.grafanaHost" .) -}}
{{- end -}}

{{- define "monitoring.oidc.systemIssuerURL" -}}
{{- $host := index .Values._cluster "root-host" -}}
{{- printf "https://keycloak.%s/realms/cozy" $host -}}
{{- end -}}

{{- /*
  Grafana `role_attribute_path` is a JMESPath expression evaluated on
  the `groups` claim. We emit a chain that maps membership in the
  per-instance -admin / -editor / -viewer groups to Grafana's built-in
  Admin / Editor / Viewer roles, defaulting to Viewer for authenticated
  identities with none of the three groups. Group naming mirrors
  clientId so a Keycloak operator provisions three groups per release.
*/}}
{{- define "monitoring.oidc.roleAttributePath" -}}
{{-   $admin  := printf "%s-%s-admin"  .Release.Namespace .Release.Name -}}
{{-   $editor := printf "%s-%s-editor" .Release.Namespace .Release.Name -}}
{{-   $viewer := printf "%s-%s-viewer" .Release.Namespace .Release.Name -}}
{{-   printf "contains(groups[*], '%s') && 'Admin' || contains(groups[*], '%s') && 'Editor' || contains(groups[*], '%s') && 'Viewer' || 'Viewer'" $admin $editor $viewer -}}
{{- end -}}

{{- /*
  Whether to promote `<clientId>-admin` group members to server-level
  `GrafanaAdmin` (Grafana's `allow_assign_grafana_admin` flag). Driven
  by an explicit values field so the platform bundle can flip it on
  for the platform release via
  `Package.spec.components["monitoring-system"].values.oidc.grafanaAdmin`
  without the chart having to sniff `.Release.Name`.
*/}}
{{- define "monitoring.oidc.allowAssignGrafanaAdmin" -}}
{{- $grafanaAdmin := dig "grafanaAdmin" false (.Values.oidc | default dict) -}}
{{- if $grafanaAdmin -}}
true
{{- else -}}
false
{{- end -}}
{{- end -}}

{{- /*
  Fail-fast when `mode: System` is requested without the platform-level
  OIDC feature being on. Mirrors the identical guard from the tenant
  kube-apiserver chart — see the note there for the reasoning.
*/}}
{{- define "monitoring.oidc.assertSystemEnabled" -}}
{{- $oidcEnabled := dig "oidc-enabled" "" (.Values._cluster | default dict) -}}
{{- if ne ($oidcEnabled | toString) "true" -}}
{{-   fail "spec.oidc.mode: System requires the platform-level OIDC feature (authentication.oidc.enabled) — enable it in cozystack-values, or use mode: CustomConfig for a tenant-supplied issuer." -}}
{{- end -}}
{{- end -}}

{{- /*
  Fail-fast when the CustomConfig branch has neither or both payloads
  set — mutually exclusive.
*/}}
{{- define "monitoring.oidc.assertCustomConfigXor" -}}
{{- $inline := (.Values.oidc.customConfig.config | default dict) -}}
{{- $secretName := dig "secretRef" "name" "" (.Values.oidc.customConfig | default dict) -}}
{{- $hasInline := gt (len $inline) 0 -}}
{{- $hasSecretRef := ne ($secretName | toString) "" -}}
{{- if and $hasInline $hasSecretRef -}}
{{-   fail "spec.oidc.customConfig: set exactly one of `config` (inline) or `secretRef.name` — they are mutually exclusive." -}}
{{- end -}}
{{- if not (or $hasInline $hasSecretRef) -}}
{{-   fail "spec.oidc.mode: CustomConfig requires either spec.oidc.customConfig.config (inline generic_oauth map) or spec.oidc.customConfig.secretRef.name (Secret with an `auth.ini` key)." -}}
{{- end -}}
{{- end -}}
