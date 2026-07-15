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
Effective worker node groups.

The default "md0" group is applied here, in the template, only when the user
supplies no nodeGroups at all. Keeping the default out of values.yaml makes
user-supplied nodeGroups authoritative: a Helm values merge would otherwise
re-add a baked-in default md0 on top of the user's groups, and because
Kubernetes strips null values the default could never be removed. With the
default applied only when the map is empty, users can freely choose their own
node groups (and omit md0).

The default carries `minReplicas: 0`, which now propagates through to
`MachineDeployment.spec.replicas: 0` (see the templates/cluster.yaml comment
next to that field). Consequence: an empty-`nodeGroups` install provisions
NO workers until either the cluster-autoscaler responds to an unschedulable
Pod or an operator explicitly scales the group up. If the operator plans to
enable the ingress-nginx addon, either supply an explicit nodeGroup with
`roles: [ingress-nginx]` and `minReplicas >= 1`, or accept that the
autoscaler will bring the default md0 up in response to the ingress-nginx
controller Pods becoming Pending on install.
*/}}
{{- define "kubernetes.nodeGroups" -}}
{{- if .Values.nodeGroups -}}
{{ toYaml .Values.nodeGroups }}
{{- else -}}
md0:
  minReplicas: 0
  maxReplicas: 10
  instanceType: "u1.medium"
  diskSize: 20Gi
  storageClass: ""
  roles:
  - ingress-nginx
  resources: {}
  gpus: []
  kubelet: {}
{{- end -}}
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
{{- printf "https://keycloak.%s/realms/cozy" (dig "root-host" "" (.Values._cluster | default dict)) }}
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
