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
Effective Talos `machine.kernel.modules` list for one worker node group.

Byte-for-byte the same helper as the parent kubernetes chart's — both charts
emit the worker machine config and must agree on it. The duplication is the
existing pattern for `kubernetes.*` helpers in this chart (the two charts do
not share a library), and tests/render-parity.sh now compares the machine
config the two talos-reconcile Jobs apply, so a divergence here fails a test
instead of shipping.

Takes the node group dict, returns a YAML list (empty output when there is
nothing to load, so the caller can gate the whole `kernel:` block on it).

A Talos system extension ships a kernel module but does not load it — that is
`machine.kernel.modules`' job. The NVIDIA extensions are the case that made
this surface necessary: without the modules the driver never initialises, and
the failure is silent all the way down (the VM has the PCI device, the node
advertises no GPU, nothing logs an error).

Three-state contract on the pool's `kernelModules`:

  unset      the chart decides. A pool holding at least one `nvidia.com/*`
             GPU gets the NVIDIA set below; anything else gets nothing.
  non-empty  taken verbatim, replacing whatever the chart would have picked.
  []         explicit opt-out — emit no `kernel` block even on a GPU pool.

The `[]` state is why the field has no entry in values.yaml at all: a default
of `[]` there would collapse "unset" into "opted out" on every pool and make
the automatic NVIDIA set unreachable, and a bare `kernelModules:` (null) fails
values.schema.json validation under helm-unittest, which sees the null before
Helm's coalescing drops it.

Module order is the order Talos' own NVIDIA documentation prescribes and the
order validated against a production GB202 passthrough node: `nvidia` first
(the others depend on it), then `nvidia_uvm` (CUDA unified memory, needed by
any workload using the CUDA runtime), then `nvidia_drm` and `nvidia_modeset`.
Talos loads them in list order, so this is not cosmetic.

Note this only covers loading the module the OS already carries. Which
extension supplies it is the schematic's business (`talos.schematicID`) and
is not derivable from the node group — on Blackwell (GB202) specifically it
has to be the open-kernel-modules extension, since the proprietary one loads,
creates `/dev/nvidia0`, and then finds no devices.
*/}}
{{- define "kubernetes.kernelModules" -}}
{{- $group := . -}}
{{- $modules := list -}}
{{- if kindIs "slice" $group.kernelModules -}}
{{-   $modules = $group.kernelModules -}}
{{- else -}}
{{-   range $group.gpus | default list -}}
{{-     if hasPrefix "nvidia.com/" (.name | default "") -}}
{{-       $modules = list (dict "name" "nvidia") (dict "name" "nvidia_uvm") (dict "name" "nvidia_drm") (dict "name" "nvidia_modeset") -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- if $modules -}}
{{ toYaml $modules }}
{{- end -}}
{{- end }}

{{/*
Effective Talos image-factory schematic ID for this worker pool.

Byte-for-byte the same helper as the parent kubernetes chart's, for the same
reason as kubernetes.kernelModules: both charts render the boot disk image and
the installer image, and tests/render-parity.sh compares both.

Takes a context carrying .group (the pool) and .Values, returns the pool's own
schematicID when set, otherwise the cluster-wide talos.schematicID.

Why this needs to be per-pool at all: a schematic is a fixed set of system
extensions baked into one image, and Talos refuses to finish booting when an
extension service in it cannot start. `ext-nvidia-persistenced` and
`ext-nvidia-cdi-gen` need an NVIDIA card, so a node with no GPU that boots the
NVIDIA schematic fails `startAllServices` and reboots, every time, forever.
Observed in production as a ~70 minute reboot cycle on a non-GPU pool in a
cluster whose only GPU pool had forced the cluster-wide schematic to an NVIDIA
one. The nodes stay `Ready` throughout — kubelet starts before the failing
phase — so `kubectl get nodes` shows nothing and only the pod restart counters
betray it.

With one schematic per cluster, a cluster that mixes GPU and non-GPU pools has
no correct value to set: the GPU pool needs the extensions and every other pool
is broken by them. This field is what makes such a cluster expressible.

Both consumers of the schematic have to agree on it, or an in-place upgrade
silently swaps a node's extension set: the boot disk image the DataVolume pulls
(nodegroup.yaml) and the installer image in the TalosConfigTemplate
(talos-reconcile-job.yaml). Both call this helper.

Deliberately NOT auto-derived from `gpus`, unlike kernelModules: a schematic ID
is an opaque image-factory digest, so the chart cannot know which one carries
the NVIDIA extensions, or synthesise one. That part stays the operator's to
supply.

Leaving it unset renders byte-identical output to a chart without this field,
so the pool's content-hash-named KubevirtMachineTemplate keeps its name and its
workers are not rolled. Setting it does roll that pool's workers, which is
inherent: changing a node's boot image means replacing the node.
*/}}
{{- define "kubernetes.schematicID" -}}
{{- .group.schematicID | default .Values.talos.schematicID -}}
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
