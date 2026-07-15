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
  Space-separated list of the four tenant-scoped Keycloak groups the
  Monitoring release's Grafana will accept on login (allowed_groups in
  auth.generic_oauth). The tenant chart (packages/apps/tenant) already
  provisions `<namespace>-{view,use,admin,super-admin}` KeycloakRealmGroups
  in the `cozy` realm — the namespace name is the tenant identifier for
  both root and nested tenants (see tenant.name in the tenant chart
  helpers), so `.Release.Namespace` is the correct prefix here.

  Why all four groups instead of just those referenced in
  `spec.oidc.users[].role`: the users-map controls Grafana org role
  assignment, not authentication. A tenant admin whose email is not in
  users[] should still be able to log in and land at the release's
  configured hands-off default (auto_assign_org_role=Viewer) — being a
  member of the tenant is the credential that says "you belong to this
  Grafana", regardless of whether the chart also happens to have
  pre-provisioned them an org role.
*/}}
{{- define "monitoring.oidc.allowedGroups" -}}
{{- $ns := .Release.Namespace -}}
{{- printf "%s-view %s-use %s-admin %s-super-admin" $ns $ns $ns $ns -}}
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

{{- /*
  Prologue that all templates touching spec.oidc.users should include
  ONCE, near the top. Runs the incompatibility check (secretRef + users)
  before the per-entry regexes so operators see the most actionable
  error first — combining incompatible top-level fields is a
  configuration bug the operator can fix immediately; a per-user
  malformed email is a data-entry issue in the CR body.
*/}}
{{- define "monitoring.oidc.assertUsersPrologue" -}}
{{-   include "monitoring.oidc.assertCustomSecretRefUsersEmpty" . }}
{{-   include "monitoring.oidc.assertUsersRoleShape" . }}
{{-   include "monitoring.oidc.assertUsersEmailShape" . }}
{{- end -}}

{{- /*
  Reject `spec.oidc.users[]` entries missing `role` or carrying an
  unknown role. openAPISchema also marks `role` required, but a stale
  schema (dashboard-emitted CR generated before the field was added, or
  a direct-helm invocation that bypasses the API validation) would
  otherwise render `role: "null"` into the users-Job's Grafana admin API
  body and fail with HTTP 400 at hook execution. Failing at render
  time surfaces the root cause instead of a mysterious backoffLimit
  exhaustion.
*/}}
{{- define "monitoring.oidc.assertUsersRoleShape" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $users := $oidc.users | default (list) -}}
{{- $allowed := list "Admin" "Editor" "Viewer" -}}
{{- range $i, $u := $users -}}
{{-   $role := $u.role | default "" -}}
{{-   if eq $role "" -}}
{{-     fail (printf "spec.oidc.users[%d].role: is required and must be one of Admin, Editor, Viewer. Omitting the field would render null into the Grafana admin API body and fail the post-install users-Job with HTTP 400." $i) -}}
{{-   end -}}
{{-   if not (has $role $allowed) -}}
{{-     fail (printf "spec.oidc.users[%d].role: %q is not one of the allowed values Admin, Editor, Viewer." $i $role) -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{- /*
  Reject malformed emails in `spec.oidc.users`. Grafana passes the
  string verbatim into a query string (/api/users/lookup?loginOrEmail=)
  and into JSON bodies (create_body / add_body); the users-Job's shell
  reader is NUL-safe and URL-encodes, but rejecting at render time is
  cheaper than debugging a 400 from Grafana. Pattern is intentionally
  conservative — no whitespace, no quoted literals, no bracketed IP
  domains, no unicode; RFC 5322 in its full generality is out of scope.
*/}}
{{- define "monitoring.oidc.assertUsersEmailShape" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $users := $oidc.users | default (list) -}}
{{- range $i, $u := $users -}}
{{-   $email := $u.email | default "" -}}
{{-   if not (regexMatch "^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$" $email) -}}
{{-     fail (printf "spec.oidc.users[%d].email: %q does not match the conservative email pattern ^[A-Za-z0-9._%%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}$. Whitespace, quoted literals, and non-ASCII characters are rejected on template level so the reconcile-Job's shell handling never has to defend against malformed input." $i $email) -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{- /*
  Reject `spec.oidc.users` in CustomConfig+secretRef mode: the
  operator's `auth.ini` fragment is authoritative and the chart cannot
  inject `skip_org_role_sync=true` / `oauth_allow_insecure_email_lookup=true`
  into it. Without those two settings the users-Job's role assignments
  are overwritten on the operator's next login (skip_org_role_sync) and
  the pre-provisioned local account is orphaned (email lookup) — the
  Job's contract silently breaks. Force the operator into an explicit
  choice: either use `customConfig.config` (inline) and let the chart
  merge the two settings, or omit `spec.oidc.users` and manage
  authorization themselves inside the mounted ini.
*/}}
{{- define "monitoring.oidc.assertCustomSecretRefUsersEmpty" -}}
{{- $oidc := .Values.oidc | default dict -}}
{{- $customConfig := $oidc.customConfig | default dict -}}
{{- $secretName := dig "secretRef" "name" "" $customConfig -}}
{{- $users := $oidc.users | default (list) -}}
{{- if and (ne ($secretName | toString) "") (gt (len $users) 0) -}}
{{-   fail "spec.oidc: `users` is not honoured under `customConfig.secretRef` — the operator's mounted auth.ini is authoritative and the chart cannot inject `skip_org_role_sync=true` / `oauth_allow_insecure_email_lookup=true`, so the users-Job's role assignments would be overwritten on the operator's next login. Either switch to `customConfig.config` (inline map, merged with the chart-forced settings) or unset `users` and manage authorization inside the ini fragment yourself." -}}
{{- end -}}
{{- end -}}
