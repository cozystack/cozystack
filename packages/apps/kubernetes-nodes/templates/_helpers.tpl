{{/*
Expand the name of the chart.
*/}}
{{- define "kubernetes.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
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
Pinned to Kamaji's default `networkProfile.clusterDomain`. Kept identical to
the parent kubernetes chart so the worker machineconfig this chart applies
matches the control plane it joins.

Distinct from .Values._cluster["cluster-domain"], which is the MANAGEMENT
cluster domain (e.g. cozy.local) where the Kamaji control plane lives.
*/}}
{{- define "kubernetes.tenantClusterDomain" -}}
cluster.local
{{- end }}

{{/*
Reconstruct the parent CAPI cluster name from the linkage value.

The pool attaches to the parent Kubernetes CR named .Values.cluster, whose
HelmRelease (and therefore CAPI Cluster / KamajiControlPlane / KubevirtCluster
and every worker object it owns) is `kubernetes-<cluster>`. This chart does
NOT use its own .Release.Name for CAPI wiring — the pool lives in a separate
HelmRelease from the control plane, so every reference that the monolithic
chart made through $.Release.Name is reconstructed here as kubernetes-<cluster>
instead. Linkage is by name convention (mirrors vm-instance -> vm-disk), not
ownerReference or lookup-gated render.
*/}}
{{- define "kubernetes-nodes.clusterName" -}}
{{- if not .Values.cluster -}}
{{- fail "kubernetes-nodes: .Values.cluster is required — set it to the parent Kubernetes CR name so the pool attaches to cluster kubernetes-<cluster>" -}}
{{- end -}}
{{- printf "kubernetes-%s" .Values.cluster -}}
{{- end -}}

{{/*
The node-group name for this pool, derived from the release name.

A KubernetesNodes CR is named <cluster>-<pool> and gets the release prefix
`kubernetes-nodes-`, so the release name is `kubernetes-nodes-<cluster>-<pool>`.
The group name is the <pool> suffix. Enforcing the `<cluster>-` segment keeps
every rendered object named `kubernetes-<cluster>-<pool>` — byte-identical to
what the monolithic chart rendered for the same group — and prevents two
clusters in one namespace from colliding on a pool named e.g. `md0`.
*/}}
{{- define "kubernetes-nodes.groupName" -}}
{{- $prefix := printf "kubernetes-nodes-%s-" .Values.cluster -}}
{{- if not (hasPrefix $prefix .Release.Name) -}}
{{- fail (printf "kubernetes-nodes: release name %q must start with %q — name the KubernetesNodes CR <cluster>-<pool> (cluster=%q)" .Release.Name $prefix .Values.cluster) -}}
{{- end -}}
{{- trimPrefix $prefix .Release.Name -}}
{{- end -}}

{{/*
Fail early with a clear message if this pool's MachineDeployment already exists
under a different Helm release — i.e. the pool name collides with a nodeGroup
still managed by the parent kubernetes chart (most likely the default `md0`).
Without this guard the collision surfaces as a cryptic Helm "invalid ownership
metadata" error at install time. Inert under `helm template`/unittest (lookup
returns nil with no cluster) and a no-op during the Phase 2b adoption
migration, which re-annotates the MachineDeployment onto this release before
it reconciles.
*/}}
{{- define "kubernetes-nodes.assertNoForeignPool" -}}
{{- $clusterName := include "kubernetes-nodes.clusterName" . -}}
{{- $groupName := include "kubernetes-nodes.groupName" . -}}
{{- $mdName := printf "%s-%s" $clusterName $groupName -}}
{{- $existing := lookup "cluster.x-k8s.io/v1beta1" "MachineDeployment" .Release.Namespace $mdName -}}
{{- if $existing -}}
{{- $owner := dig "annotations" "meta.helm.sh/release-name" "" $existing.metadata -}}
{{- if and $owner (ne $owner .Release.Name) -}}
{{- fail (printf "kubernetes-nodes: MachineDeployment %q in namespace %q is already managed by release %q, not this pool release %q — the pool name collides with a nodeGroup still managed by the parent kubernetes chart. Rename the pool or remove it from the parent Kubernetes CR's nodeGroups first." $mdName .Release.Namespace $owner .Release.Name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
