{{- /*
  Helpers for the Grafana OIDC integration.

  Naming — the identity unit is the Monitoring release, so the clientId
  and the audience scope are `<namespace>-<release>` prefixed. That
  gives:
    tenant-foo/monitoring-system   (per-tenant Grafana in tenant-foo ns)
    tenant-root/monitoring-system  (platform Grafana in the tenant-root ns)
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
{{- $namespaceHost := include "cozy-lib.ns-host-required" . -}}
{{- printf "grafana.%s" (.Values.host | default $namespaceHost) -}}
{{- end -}}

{{- define "monitoring.oidc.redirectUri" -}}
{{- printf "https://%s/login/generic_oauth" (include "monitoring.oidc.grafanaHost" .) -}}
{{- end -}}

{{- define "monitoring.oidc.systemIssuerURL" -}}
{{- $host := index .Values._cluster "root-host" -}}
{{- $realm := index .Values._cluster "oidc-realm-name" | default "cozy" | toString -}}
{{- printf "https://keycloak.%s/realms/%s" $host $realm -}}
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

{{- /*
  Values here reach the VictoriaTraces CRs unchecked: the installed CRDs are the
  trimmed ones (crds.plain: false), so spec is x-kubernetes-preserve-unknown-fields.
    - name is at most 42 characters: the operator names the StatefulSet
      vtstorage-<name>, and each pod carries the label controller-revision-hash:
      vtstorage-<name>-<hash> (hash up to 10 characters, value capped at 63).
    - storage is positive: in single mode the operator mounts an EmptyDir when
      the requested size IsZero, so the store reports operational with no volume.
    - retentionDiskUsageBytes uses the operator's BytesString grammar, which
      rejects the Kubernetes-quantity `Gi` suffix that storage uses.
*/ -}}
{{- define "monitoring.tracingStorages.validate" -}}
{{- $seen := dict -}}
{{- range $i, $s := .Values.tracingStorages -}}
{{-   $name := $s.name | default "" | toString -}}
{{-   if or (not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" $name)) (gt (len $name) 42) -}}
{{-     fail (printf "monitoring: tracingStorages[%d].name %q must be a valid RFC 1123 label (lowercase alphanumeric and '-', starting and ending alphanumeric) of at most 42 characters, so that the vtstorage-<name>-<hash> pod revision label stays within 63." $i $name) -}}
{{-   end -}}
{{-   if hasKey $seen $name -}}
{{-     fail (printf "monitoring: tracingStorages[%d].name %q is duplicated — names must be unique across tracingStorages, since each keys a VTCluster/VTSingle and a GrafanaDatasource and a collision silently overwrites the first." $i $name) -}}
{{-   end -}}
{{-   $_ := set $seen $name true -}}
{{-   $mode := $s.mode | default "cluster" -}}
{{-   if and (ne $mode "cluster") (ne $mode "single") -}}
{{-     fail (printf "monitoring: tracingStorages[%s].mode must be either \"cluster\" or \"single\"" $name) -}}
{{-   end -}}
{{-   $storage := $s.storage | default "" | toString -}}
{{-   if or (regexMatch "^[+-]?[0.]*([KMGTPE]i|[numkMGTPE]|[eE][+-]?[0-9]+)?$" $storage) (hasPrefix "-" $storage) -}}
{{-     fail (printf "monitoring: tracingStorages[%s].storage must be a positive Kubernetes quantity (e.g. 10Gi). A zero size makes the operator mount an EmptyDir instead of a PVC in single mode, so the backend reports Ready while every stored span is lost on the next reschedule; a negative one fails the release when the PVC is created." $name) -}}
{{-   end -}}
{{-   with $s.retentionDiskUsageBytes -}}
{{-     $bytes := . | toString -}}
{{-     if not (regexMatch "^[0-9]+(kb|mb|gb|tb|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$" $bytes) -}}
{{-       fail (printf "monitoring: tracingStorages[%s].retentionDiskUsageBytes %q is not a valid VictoriaMetrics BytesString (^[0-9]+(kb|mb|gb|tb|KB|MB|GB|TB|KiB|MiB|GiB|TiB)?$). Its units differ from the sibling `storage` field (a Kubernetes quantity like 10Gi): write 8GB or 8GiB, not 8Gi." $name $bytes) -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{- /*
  The shared VictoriaTraces store separates tenants only by the AccountID and
  ProjectID request headers (two uint32s), so each tenant needs a stable pair it
  cannot choose. It is derived from the namespace: the first 16 hex digits of
  the sha256 of its name and UID, split into two uint32s, which needs no
  allocator and no stored state. The UID keeps a tenant re-created under a
  deleted tenant's name out of that tenant's retained spans; an offline render
  has no UID and hashes the name alone. tenant-root's own spans stay in the
  header-less default 0:0, since its collector always writes locally. Renders
  "<AccountID>:<ProjectID>".
*/ -}}
{{- define "monitoring.tracingTenantID" -}}
{{- $digits := dict "0" 0 "1" 1 "2" 2 "3" 3 "4" 4 "5" 5 "6" 6 "7" 7 "8" 8 "9" 9 "a" 10 "b" 11 "c" 12 "d" 13 "e" 14 "f" 15 -}}
{{- $key := .Release.Namespace -}}
{{- with dig "metadata" "uid" "" (lookup "v1" "Namespace" "" .Release.Namespace) -}}
{{-   $key = printf "%s:%s" $key . -}}
{{- end -}}
{{- $sum := sha256sum $key -}}
{{- $ids := list -}}
{{- range $part := list (substr 0 8 $sum) (substr 8 16 $sum) -}}
{{-   $n := 0 -}}
{{-   range $c := splitList "" $part -}}
{{-     $n = add (mul $n 16) (get $digits $c) -}}
{{-   end -}}
{{-   $ids = append $ids $n -}}
{{- end -}}
{{- printf "%d:%d" (index $ids 0 | int64) (index $ids 1 | int64) -}}
{{- end -}}

{{- /*
  The central tenant's vmauth password: the one already stored, else a new
  random one. Both the Secret and the collector's rollout checksum read it, and
  randAlphaNum differs per call, so the first result is kept in .Values, which
  every template of one render shares.
*/ -}}
{{- define "monitoring.tracingCentralPassword" -}}
{{- $ctx := index . 0 -}}
{{- $secret := index . 1 -}}
{{- $key := printf "_tracingCentralPassword_%s" $secret -}}
{{- if not (hasKey $ctx.Values $key) -}}
{{-   $password := "" -}}
{{-   with (index (lookup "v1" "Secret" $ctx.Release.Namespace $secret) "data") -}}
{{-     with .password }}{{ $password = b64dec . }}{{ end -}}
{{-   end -}}
{{-   if not $password }}{{ $password = randAlphaNum 32 }}{{ end -}}
{{-   $_ := set $ctx.Values $key $password -}}
{{- end -}}
{{- index $ctx.Values $key -}}
{{- end -}}

{{- /* tenant-root hosts the shared store only when it opts in with
       tracingCentralHost, since the store is also its own, and lists a
       cluster-mode entry named generic, the store every central VMUser routes
       to. Renders "true" or "". */}}
{{- define "monitoring.tracingCentralStore" -}}
{{- if and (eq .Release.Namespace "tenant-root") (eq (toString .Values.tracingCentralHost) "true") -}}
{{-   range .Values.tracingStorages -}}
{{-     if and (eq .name "generic") (eq (.mode | default "cluster") "cluster") }}true{{ end -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{- /*
  Shared-central tracing is switched on only by the tenant chart, through
  _namespace in the cozystack-values Secret: the apps API refuses `_` keys, so a
  Monitoring application cannot opt itself in without the egress rules the
  tenant chart renders under the same condition. tenant-root hosts the shared
  store and never exports to it. Renders "true" or "".
*/ -}}
{{- define "monitoring.tracingCentral" -}}
{{- if and (ne .Release.Namespace "tenant-root") (eq (toString (dig "tracingCentral" false (.Values._namespace | default dict))) "true") -}}true{{- end -}}
{{- end -}}
