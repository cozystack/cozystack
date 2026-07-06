{{- /*
  Helpers for the Grafana OIDC integration.

  Naming — the identity unit is the Monitoring release, so the clientId
  and the audience scope are `<namespace>-<release>` prefixed. That
  gives:
    tenant-foo/monitoring        (per-tenant Grafana in tenant-foo ns)
    cozy-monitoring/monitoring-system  (platform Grafana in cozy-monitoring ns)
  A token minted for one instance is rejected by the other's Grafana
  because the per-cluster audience scope binds `id_token.aud` to the
  clientId — same isolation primitive as the tenant kube-apiserver PR
  (cozystack/cozystack#3044).

  Authorization is NOT modelled on Keycloak realm groups: the chart
  does not own directory objects and Grafana's built-in group→role
  mapping (`role_attribute_path`) is not wired up. Instead, `spec.oidc`
  carries a `users:` map and a chart-owned post-install/post-upgrade
  Job reconciles that map into Grafana org membership/roles via the
  admin API. `skip_org_role_sync = true` prevents a login from
  overwriting those app-side assignments. Same operator UX as the
  tenant kube-apiserver sibling in #3044.
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
  Fail-fast when `mode: System` is requested but the Keycloak operator
  CRDs (`v1.edp.epam.com/v1`) are not yet registered in the target
  cluster. Without this guard the chart would silently drop the whole
  Keycloak side (KeycloakClient / KeycloakClientScope) while still
  rendering Grafana's `auth.generic_oauth` block pointing at a client
  that never gets provisioned — a broken login path with no clear
  error. For the platform `monitoring-system` release the `oidc`
  variant of `cozystack.monitoring-application` waits on
  `cozystack.keycloak-operator` (see
  packages/core/platform/sources/monitoring-application.yaml) so the
  CRDs are registered before this chart reconciles; the assertion
  turns any residual race (or a manual `mode: System` toggle on a
  cluster where the operator is not deployed) into an actionable
  render error instead of a silent misconfiguration.
*/}}
{{- define "monitoring.oidc.assertSystemKeycloakCRD" -}}
{{- if not (.Capabilities.APIVersions.Has "v1.edp.epam.com/v1") -}}
{{-   fail "spec.oidc.mode: System requires the Keycloak operator CRDs (v1.edp.epam.com/v1). If cozystack.keycloak-operator is still bootstrapping this will resolve on the next reconcile; otherwise verify the keycloak-operator package is deployed and its CRDs are registered." -}}
{{- end -}}
{{- end -}}

{{- /*
  Fail-fast when the CustomConfig branch has neither or both payloads
  set — mutually exclusive.
*/}}
{{- define "monitoring.oidc.assertCustomConfigXor" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $customConfig := $oidc.customConfig | default dict -}}
{{- $inline := $customConfig.config | default dict -}}
{{- $secretName := dig "secretRef" "name" "" $customConfig -}}
{{- $hasInline := gt (len $inline) 0 -}}
{{- $hasSecretRef := ne ($secretName | toString) "" -}}
{{- if and $hasInline $hasSecretRef -}}
{{-   fail "spec.oidc.customConfig: set exactly one of `config` (inline) or `secretRef.name` — they are mutually exclusive." -}}
{{- end -}}
{{- if not (or $hasInline $hasSecretRef) -}}
{{-   fail "spec.oidc.mode: CustomConfig requires either spec.oidc.customConfig.config (inline generic_oauth map) or spec.oidc.customConfig.secretRef.name (Secret with an `auth.ini` key)." -}}
{{- end -}}
{{- end -}}

{{- /*
  Normalised `spec.oidc.users` list. Yields an empty list when
  `spec.oidc` is omitted, `null`, or has no `users:` key. Consumers
  should range over this without further nil-checks.
*/}}
{{- define "monitoring.oidc.users" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $users := $oidc.users | default (list) -}}
{{- toYaml $users -}}
{{- end -}}
