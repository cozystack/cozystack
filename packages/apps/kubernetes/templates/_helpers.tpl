{{/*
Expand the name of the chart.
*/}}
{{- define "kubernetes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "kubernetes.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "kubernetes.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "kubernetes.labels" -}}
helm.sh/chart: {{ include "kubernetes.chart" . }}
{{ include "kubernetes.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubernetes.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubernetes.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
DNS domain used INSIDE the tenant cluster (kubelet --cluster-domain,
apiserver --service-cluster-ip-range FQDNs, CoreDNS authoritative zone).
Pinned to Kamaji's default `networkProfile.clusterDomain` since this chart
does not currently expose a knob to override it. If that ever becomes
configurable, plumb the override here and every consumer picks it up.

Distinct from .Values._cluster["cluster-domain"], which is the MANAGEMENT
cluster domain (e.g. cozy.local) where the Kamaji control plane and
monitoring stack live.
*/}}
{{- define "kubernetes.tenantClusterDomain" -}}
cluster.local
{{- end }}

{{/*
wait-for-kubeconfig init container shared by the control-plane-side
Deployments (cluster-autoscaler, kccm, kcsi-controller) that mount the
*-admin-kubeconfig Secret provisioned asynchronously by Kamaji. The
Secret volume is declared optional so kubelet does not FailedMount while
Kamaji is still bootstrapping; this container polls the mounted path and
exits only when super-admin.svc appears, which happens after kubelet's
optional-Secret refresh cycle.

The 10m deadline stays strictly below the 20m HelmRelease
Install.Timeout set by cozystack-api for the Kubernetes kind (via the
release.cozystack.io/helm-install-timeout annotation on the cozyrds
entry) so the CrashLoopBackOff surfaces before flux remediation fires and uninstalls
the Cluster CR.

The default image lives in images/busybox.tag and points directly at
docker.io by digest (not mirrored to ghcr.io like the other .tag files
here): the payload is a one-shot sh loop and the digest pin makes the
pull immutable. Operators in air-gapped or rate-limited environments
can override it via .Values.images.waitForKubeconfig (any registry
reference kubelet can pull). When the value is empty the chart falls
back to the bundled digest pin, preserving the prior default.

Call site owns the surrounding volumes block; the kubeconfig volume
must exist on the pod and mount at /etc/kubernetes/kubeconfig.
*/}}
{{- define "kubernetes.waitForAdminKubeconfig" -}}
- name: wait-for-kubeconfig
  image: "{{ default (.Files.Get "images/busybox.tag" | trim) .Values.images.waitForKubeconfig }}"
  command:
  - sh
  - -c
  - |
    set -eu
    deadline=$(( $(date +%s) + 600 ))
    until [ -s /etc/kubernetes/kubeconfig/super-admin.svc ]; do
      if [ "$(date +%s)" -ge "$deadline" ]; then
        echo "admin kubeconfig was not provisioned within 10m; exiting so the pod goes CrashLoopBackOff and surfaces in dashboards" >&2
        exit 1
      fi
      echo "waiting for admin kubeconfig (provisioned by Kamaji, visible after kubelet Secret refresh)..."
      sleep 5
    done
  volumeMounts:
  - name: kubeconfig
    mountPath: /etc/kubernetes/kubeconfig
    readOnly: true
{{- end }}

{{/*
OIDC clientId for the per-cluster Keycloak public client (mode: System).

Namespaced by Release.Namespace so the identifier is globally unique within
the `cozy` realm — two clusters of the same name in different tenants would
otherwise collide. The audience binding (KeycloakClientScope) and the
apiserver's `AuthenticationConfiguration` audience use this same value, so
both ends of the per-cluster isolation primitive line up by construction.

Truncated to 253 characters because the EDP Keycloak operator stores the
client as a Kubernetes CR named after the clientId (DNS-1123 subdomain).
*/}}
{{- define "kubernetes.oidc.clientId" -}}
{{- printf "%s-%s" .Release.Namespace .Release.Name | trunc 253 | trimSuffix "-" }}
{{- end }}

{{/*
Name of the per-cluster KeycloakClientScope that carries the audience
mapper. Same uniqueness considerations as the clientId; suffixed with
`-audience` so it does not collide with the global `kubernetes-client`
scope from packages/system/keycloak-configure.
*/}}
{{- define "kubernetes.oidc.audienceScopeName" -}}
{{- printf "%s-%s-audience" .Release.Namespace .Release.Name | trunc 253 | trimSuffix "-" }}
{{- end }}

{{/*
Issuer URL for `mode: System`. Resolves to the platform Keycloak realm
`cozy`, served at the root host published in the per-namespace bundle by
cozystack-basics.
*/}}
{{- define "kubernetes.oidc.systemIssuerURL" -}}
{{- printf "https://keycloak.%s/realms/%s" (dig "root-host" "" (.Values._cluster | default dict)) (dig "oidc-realm-name" "cozy" (.Values._cluster | default dict) | toString) }}
{{- end }}

{{/*
CEL claimValidationRule body — rejects tokens whose `groups` claim does
NOT carry at least one of the tenant's four Keycloak groups. The tenant
chart (packages/apps/tenant) provisions these groups per-tenant in the
`cozy` realm; the namespace name is the tenant identifier for both root
and nested tenants (see tenant.name in the tenant chart helpers), so
`.Release.Namespace` is the correct prefix.

The `has(claims.groups) &&` guard is required — CEL evaluation of
`claims.groups.exists(...)` on a token missing the claim raises a
runtime error that surfaces as HTTP 500 from the authenticator rather
than the intended 401. `has()` short-circuits that path into a plain
unauthorized outcome with the `message` string in the audit log.

Why enforce membership at the apiserver even though RBAC default-denies
unmapped identities: `system:authenticated` still leaks the OpenAPI +
discovery surface (kubectl auth can-i --list, kubectl api-resources,
`/apis/*` schemata) to every user in the shared `cozy` realm — a
tenant-alice user could enumerate tenant-bob's cluster's CRDs and
built-in resource shape. Adding a hard cross-tenant gate here matches
the design's stated authorization boundary (per-tenant kube-apiserver
= per-tenant identity domain) rather than relying on downstream
RBAC-shaped conservatism.
*/}}
{{- /*
`claims.groups` is statically typed `any` in the apiserver's CEL environment
(claims is map(string, any)), and the `.exists()` comprehension macro rejects a
range of type `any` — the apiserver fails AuthenticationConfiguration compilation
at startup and CrashLoops ("expression of type 'any' cannot be range of a
comprehension (must be list, map, or dynamic)"). Wrap in dyn() so CEL treats the
range as dynamic; the `has()` guard still short-circuits when groups is absent.
Verified on a live tenant apiserver (v1.32) — without dyn() the control plane
never boots.
*/ -}}
{{- define "kubernetes.oidc.groupsClaimValidationExpr" -}}
{{- $ns := .Release.Namespace -}}
{{- printf "has(claims.groups) && dyn(claims.groups).exists(g, g in [\"%s-view\", \"%s-use\", \"%s-admin\", \"%s-super-admin\"])" $ns $ns $ns $ns -}}
{{- end }}

{{- /*
Validates and returns a duration destined for a consumer that does not reject
a bad value: the cluster-autoscaler parses its annotation with
time.ParseDuration and silently falls back to its built-in default on a value
it cannot parse, and the CAPI webhook does not validate unhealthyConditions
timeouts at all, so a zero or negative one applies and remediates a Machine the
moment its condition flips. The values schema types these fields as strings and
stops there, which lets "30" and "-5m" reach the template. The accepted shape
is narrower than Go's duration grammar on purpose, because that grammar also
admits a value large enough to overflow the parser -- the silent fallback again
-- and a fraction small enough to round to zero. The first segment must be
positive; later segments may be zero, so the canonical rendering of a duration
("20m0s", "1h0m0s" -- what metav1.Duration serializes to) round-trips. Every
string this admits parses to a positive duration.
*/ -}}
{{- define "kubernetes.positiveDuration" -}}
{{- $value := toString .value -}}
{{- if not (regexMatch "^[1-9][0-9]{0,4}[smh]([0-9]{1,5}[smh]){0,2}$" $value) -}}
{{-   fail (printf "%s must be a whole number of s, m or h (e.g. 30m, 1h30m), got %q" .field $value) -}}
{{- end -}}
{{- $value -}}
{{- end -}}

{{/*
  The two kube-apiserver flags the tenant control plane owns that have a
  KamajiControlPlane field it renders them from, keyed to that field.
  cluster.yaml moves an --flag=value entry for either into the field.
*/}}
{{- define "kubernetes.apiServer.movedArgFields" -}}
{{- dict
      "--enable-admission-plugins" "controlPlane.apiServer.admissionControllers"
      "--kubelet-preferred-address-types" "controlPlane.kubelet.preferredAddressTypes"
    | toJson }}
{{- end }}

{{/*
  controlPlane.apiServer.extraArgs as the KamajiControlPlane carries it, as a
  JSON list: every entry except an --flag=value entry that cluster.yaml moves
  into its field. Every writer of spec.apiServer.extraArgs reads this.
*/}}
{{- define "kubernetes.apiServer.keptExtraArgs" -}}
{{- $moved := include "kubernetes.apiServer.movedArgFields" . | fromJson }}
{{- $kept := list }}
{{- range $arg := .Values.controlPlane.apiServer.extraArgs | default list }}
{{-   $flag := index (splitList "=" (toString $arg)) 0 }}
{{-   if not (and (hasKey $moved $flag) (contains "=" (toString $arg))) }}
{{-     $kept = append $kept $arg }}
{{-   end }}
{{- end }}
{{- toJson $kept }}
{{- end }}
{{- /*
  Nameservers are addresses, so each entry is checked to be one.

  Here the value reaches no shell: this chart writes it into
  ProxmoxCluster.spec.dnsServers through toYaml. It is checked all the same, so
  that a hostname or a typo is refused at the same place in both charts rather
  than accepted here and rejected in the pool chart, where the same list is
  written into the reconcile Job's unquoted heredoc and has to be an address.

  IPv6 is matched on its character set rather than its full grammar; the
  apiserver rejects a malformed address anyway.
*/ -}}
{{- define "kubernetes.assertDnsServersAreAddresses" -}}
{{- $v4 := `^((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])$` -}}
{{- range $i, $e := (default (list) .Values.proxmox.dnsServers) -}}
{{- $s := $e | toString -}}
{{- if not (or (regexMatch $v4 $s) (and (contains ":" $s) (regexMatch `^[0-9A-Fa-f:]+$` $s))) -}}
{{- fail (printf "proxmox.dnsServers[%d] is %q: entries must be IPv4 or IPv6 addresses. Talos takes addresses here, and this list is written into a shell heredoc, so anything else is both invalid for Talos and unsafe to interpolate." $i $s) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
  substrate decides which infrastructure provider this cluster's Cluster object
  points at, and it is safe to choose only once. Switching it on a live cluster
  renders no KubevirtCluster, no kccm, no kubevirt-csi controller and no
  in-tenant -csi HelmRelease — so Flux
  uninstalls the CSI driver behind every PVC the tenant already has, and the
  same render adds a ProxmoxCluster and turns the apiserver Service into a
  LoadBalancer. CAPI does not stop it: its Cluster webhook forbids removing
  spec.infrastructureRef but admits a change of kind. The `## @immutable` marker
  on the value is dashboard-only, which is why this reads the live object.

  Inert offline, like the other lookup guards here: with no apiserver the lookup
  is empty and the render proceeds, so `helm template` and the unit tests are
  unaffected and a first install has nothing to compare against.
*/ -}}
{{- define "kubernetes.assertSubstrateUnchanged" -}}
{{- $isProxmox := eq (.Values.substrate | default "kubevirt") "proxmox" -}}
{{- $wantKind := ternary "ProxmoxCluster" "KubevirtCluster" $isProxmox -}}
{{- $live := lookup "cluster.x-k8s.io/v1beta1" "Cluster" .Release.Namespace .Release.Name -}}
{{- if $live -}}
{{- $liveKind := dig "spec" "infrastructureRef" "kind" "" $live -}}
{{- if and $liveKind (ne $liveKind $wantKind) -}}
{{- fail (printf "kubernetes: cluster %q already runs on %s and substrate is now %q, which renders a %s. Switching the substrate of a live cluster removes the cloud-controller-manager, the CSI controller and the in-tenant CSI HelmRelease, so Flux uninstalls the driver behind every existing PVC. Create a new cluster on the other substrate and migrate the workloads instead." .Release.Name $liveKind (.Values.substrate | default "kubevirt") $wantKind) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
  A per-cluster owner id for the volumes this tenant's CSI controller creates.

  The driver names every volume vm-<controllerVmID>-pvc-<uuid> and defaults that
  id to 9999, so on shared storage all tenants' volumes carry one owner: an
  operator looking at leftovers cannot tell whose they are, and a VM-level ACL
  cannot separate them. Deriving it from the release name gives each tenant its
  own, which is what makes both of those possible.

  The range starts at 100000, above the ids Proxmox hands out to real guests in
  practice and well above the driver's own minimum of 100, and is 800000 wide so
  two tenants colliding takes a birthday collision rather than a near miss. It
  is derived, not stored: the same release always produces the same id, and a
  cluster that is deleted and recreated under the same name adopts its own old
  volumes rather than orphaning them.
*/ -}}
{{- define "kubernetes.proxmoxControllerVmID" -}}
{{- add 100000 (mod (atoi (adler32sum .Release.Name)) 800000) -}}
{{- end -}}
