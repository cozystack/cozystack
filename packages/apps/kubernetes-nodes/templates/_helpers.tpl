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
{{- /* clusterName is the parent cluster's HelmRelease/object name: the CAPI
       Cluster and KamajiControlPlane are named after it, and every worker
       object this chart renders (KMT/MD/MHC names, MachineDeployment
       spec.clusterName, the version-guard lookup) must reference it. The
       aggregated API names a Kubernetes cluster's release `kubernetes-<cluster>`,
       which is the default and keeps every existing pool byte-identical. A
       wrapper whose cluster release name does NOT follow that convention —
       the ComputePlane module fixes its cluster release to `computeplane-cluster`
       to satisfy the admin-kubeconfig Secret contract — sets clusterReleaseName
       so its pools attach to the right cluster. See kubernetes-nodes.groupName:
       .Values.cluster still drives the release-name prefix and error messages. */}}
{{- .Values.clusterReleaseName | default (printf "kubernetes-%s" .Values.cluster) -}}
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
{{- /* clusterName-drift guard. The release name kubernetes-nodes-<cluster>-<pool>
       does not encode where <cluster> ends and <pool> begins: for one release
       name several (.Values.cluster, pool) splits reconstruct the SAME object
       name kubernetes-<cluster>-<pool>, so an operator who edits spec.cluster to
       another such value (its immutability is enforced only by the dashboard,
       not the aggregated apiserver, see docs/storage-immutability.md) does not
       prune or delete a single worker VM, but silently flips spec.clusterName
       and the pool WorkloadMonitor selector. CAPI rejects the immutable
       MachineDeployment.spec.clusterName Update loudly; the WorkloadMonitor
       drift is the silent half. Refuse the render when our reconstructed
       clusterName disagrees with the live object's, and name the value to
       restore. Inert offline (lookup nil). */}}
{{- $liveCluster := dig "spec" "clusterName" "" $existing -}}
{{- if and $liveCluster (ne $liveCluster $clusterName) -}}
{{- fail (printf "kubernetes-nodes: MachineDeployment %q in namespace %q has spec.clusterName %q but this pool release renders clusterName %q: .Values.cluster was changed after the pool was created, and it is immutable. Object names still collide so no worker VM is pruned, but the pool's WorkloadMonitor selector would silently drift off its machines. Restore spec.cluster to %q on this pool's HelmRelease." $mdName .Release.Namespace $liveCluster $clusterName (trimPrefix "kubernetes-" $liveCluster)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
kubernetes-nodes.assertParentVersion fails the render only when the pool's
Kubernetes minor version is AHEAD of the parent cluster's. Workers must not run a
kubelet ahead of the apiserver (unsupported skew), but a worker minor lagging the
control plane is supported (up to n-3 upstream) and is the normal state during a
rolling upgrade — the parent Kubernetes CR is bumped first, then each pool. A
symmetric equality check would flip every pool of a cluster to render-failure the
moment the parent is bumped, blocking all pool operations until each pool is
hand-edited, so the check is directional. The split removed the single `version`
that used to feed both control plane and workers. Looks up the parent
KamajiControlPlane (named like the reconstructed clusterName) and compares its
spec.version minor against .Values.version. Skipped when the lookup is empty
(helm template / unittest, or the parent not yet present) so it validates only
against a real cluster and never blocks offline rendering.
*/}}
{{- define "kubernetes-nodes.assertParentVersion" -}}
{{- $clusterName := include "kubernetes-nodes.clusterName" . -}}
{{- $kcp := lookup "controlplane.cluster.x-k8s.io/v1alpha1" "KamajiControlPlane" .Release.Namespace $clusterName -}}
{{- if $kcp -}}
{{- $parentVer := dig "spec" "version" "" $kcp -}}
{{- if $parentVer -}}
{{- $parentMinor := regexFind "v?[0-9]+\\.[0-9]+" $parentVer -}}
{{- $poolMinor := regexFind "v?[0-9]+\\.[0-9]+" (.Values.version | toString) -}}
{{- if and $parentMinor $poolMinor -}}
{{- $parentNorm := printf "%s.0" (trimPrefix "v" $parentMinor) -}}
{{- $poolNorm := printf "%s.0" (trimPrefix "v" $poolMinor) -}}
{{- if semverCompare (printf "> %s" $parentNorm) $poolNorm -}}
{{- fail (printf "kubernetes-nodes: pool version %q is ahead of parent cluster %q version %q — a worker kubelet may not run ahead of the apiserver. A worker minor may lag the control plane (rolling upgrade) but not lead it; set .version to at most the parent Kubernetes CR's minor." (.Values.version | toString) $clusterName $parentVer) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
kubernetes-nodes.assertTalosSupportsKubernetes fails the render when a Talos
release is paired with a Kubernetes minor outside that Talos minor's support
window. Each Talos minor supports a bounded window of Kubernetes minors; running
a kubelet outside it produces a silently broken Talos+kubelet combination that no
HelmRelease condition can detect, so the render is where it has to be caught.
Source: https://docs.siderolabs.com/talos/v1.13/getting-started/support-matrix

A named template rather than an inline block because the pool resolves TWO Talos
versions and both reach a worker. `talos.version` is the pool default, and
`image.builtin.version` / `image.factory.version` override it for the boot disk
AND for the in-guest installer the reconcile Job writes. Checking only the first
leaves an asymmetry a reader would not expect: a known-bad pairing set through
`talos.version` is rejected, while the same pairing reached through an
`image.*.version` override renders clean. The matrix literal lives here, once, so
the two call sites cannot drift apart.

A Talos minor the matrix does not list passes. That is deliberate and unchanged:
the matrix is a hand-maintained table, and failing closed on it would block every
operator who moves to a newer Talos before this file is updated. What the guard
promises is that a pairing it KNOWS to be bad is refused, not that every pairing
is known.

Arguments: talosVersion, kubernetesVersion, remedy (what the operator should
change, appended to the message).
*/}}
{{- define "kubernetes-nodes.assertTalosSupportsKubernetes" -}}
{{- $talosK8sSupportMatrix := dict
      "v1.13" (list "v1.31" "v1.32" "v1.33" "v1.34" "v1.35" "v1.36")
-}}
{{- $talosMinor := regexFind "^v[0-9]+\\.[0-9]+" (.talosVersion | toString) -}}
{{- with index $talosK8sSupportMatrix $talosMinor -}}
{{-   if not (has ($.kubernetesVersion | toString) .) -}}
{{-     fail (printf "Kubernetes %s is not supported by Talos %s. Supported versions: %v. %s" ($.kubernetesVersion | toString) ($.talosVersion | toString) . $.remedy) -}}
{{-   end -}}
{{- end -}}
{{- end -}}

{{- /*
kubernetes-nodes.resolveOsImage resolves this pool's `osImage` selection down to
the three strings that reach a rendered manifest -- a schematic, a Talos release
and an Image Factory URL -- plus whether the boot disk is a clone of a golden or
an HTTP import.

Two templates need that answer: nodegroup.yaml builds the worker disk source from
it, and talos-reconcile-job.yaml builds the in-guest installer reference from it.
They must agree, because a disk booted from one Talos flavor and an installer
pinned to another is exactly the silently-broken pairing this chart's support
matrix was factored out to prevent, and it would surface only as a node that
upgrades itself onto the wrong OS. So the resolution lives here once rather than
being mirrored by hand in both files.

The result comes back through the caller's `out` dict rather than as text,
because four values have to return and a delimited string would need parsing at
both call sites.

The three format checks live here too, for the same reason. The values are
tenant-controlled strings that land unquoted in a KubevirtMachineTemplate and in
a DataVolume name, and the schema types all three as a bare string and cannot
narrow them: the pinned cozyvalues-gen derives `pattern` from the value TYPE
(quantity) and has no annotation for a custom one. So the check is at render
time, which is where this chart already validates the kubelet reservation fields
for the same class of hazard. The resolved values are checked, not just the
overridden ones -- an override and the pool default reach the same interpolation.
The patterns accept every form the chart ships and every form a Talos Image
Factory produces (a 64-character hex schematic, a vN.N.N release, an http(s) URL)
and reject the bytes that would break out of a YAML scalar.

Arguments: osImage (the pool's .Values.osImage), talos (.Values.talos), out (the
dict the result is written into), groupName (named in every error message).
*/ -}}
{{- define "kubernetes-nodes.resolveOsImage" -}}
{{- $img := .osImage | default dict -}}
{{- if and (hasKey $img "builtin") (hasKey $img "factory") -}}
{{-   fail (printf "nodeGroup %q: set only one of osImage.builtin or osImage.factory" .groupName) -}}
{{- end -}}
{{- /* hasKey, not truthiness: builtin/factory may be present but empty ({} means
       "clone / import with the pool's talos.* defaults"), and Go templates treat
       an empty map as false. */ -}}
{{- $schematicID := .talos.schematicID -}}
{{- $version := .talos.version -}}
{{- $factoryURL := .talos.imageFactoryURL -}}
{{- $clone := false -}}
{{- if hasKey $img "builtin" -}}
{{-   $builtin := $img.builtin | default dict -}}
{{-   $clone = true -}}
{{-   $schematicID = $builtin.schematicID | default .talos.schematicID -}}
{{-   $version = $builtin.version | default .talos.version -}}
{{- else if hasKey $img "factory" -}}
{{-   $factory := $img.factory | default dict -}}
{{-   $schematicID = $factory.schematicID | default .talos.schematicID -}}
{{-   $version = $factory.version | default .talos.version -}}
{{-   $factoryURL = $factory.imageFactoryURL | default .talos.imageFactoryURL -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" ($schematicID | toString)) -}}
{{-   fail (printf "nodeGroup %q: Talos schematicID %q is not a plain lowercase alphanumeric identifier. It is interpolated into the worker DataVolume name and the image URL, so it must carry no whitespace, path separators or YAML metacharacters." .groupName ($schematicID | toString)) -}}
{{- end -}}
{{- if not (regexMatch "^v[0-9]+\\.[0-9]+\\.[0-9]+(-[0-9a-z.]+)?$" ($version | toString)) -}}
{{-   fail (printf "nodeGroup %q: Talos version %q is not a vMAJOR.MINOR.PATCH release. It is interpolated into the worker DataVolume name and the image URL, so it must carry no whitespace or YAML metacharacters." .groupName ($version | toString)) -}}
{{- end -}}
{{- if not (regexMatch "^https?://[A-Za-z0-9._~:/?#\\[\\]@!&'()*+,;=%-]+$" ($factoryURL | toString)) -}}
{{-   fail (printf "nodeGroup %q: imageFactoryURL %q is not a plain http(s) URL. It is interpolated into the worker DataVolume source URL, so it must carry no whitespace, backtick, dollar sign or YAML metacharacters." .groupName ($factoryURL | toString)) -}}
{{- end -}}
{{- $_ := set .out "schematicID" $schematicID -}}
{{- $_ := set .out "version" $version -}}
{{- $_ := set .out "imageFactoryURL" $factoryURL -}}
{{- $_ := set .out "clone" $clone -}}
{{- end -}}

{{- /*
Name of the cluster's default StorageClass, or the empty string when there is
none (and always under `helm template`, which has no cluster to read).

An empty storageClass is a documented, schema-valid setting on both a worker pool
and a worker image catalog entry, and it means "the cluster default". Without
resolving it the golden-versus-pool StorageClass comparison simply skips whenever
either side is empty, which is the one corner where skipping is worst: CDI then
falls back to a host-assisted copy over the pod network, silently, and that copy
is the transfer the clone path exists to remove. Reviewed as [MINOR] on
cozystack/cozystack#3294.

Both the current annotation and its beta predecessor count, because clusters
provisioned years apart carry different ones and Kubernetes still honours both.

More than one class may carry the annotation at once. Kubernetes permits that --
it is the normal state midway through swapping a cluster's default -- and
resolves it by taking the most recently created, so this does the same. Emitting
every match instead concatenates their names into a class that does not exist,
and the comparison below then rejects a pool whose class matches the default
that is actually in force. Timestamps are RFC3339 in UTC, so comparing them as
strings is comparing them chronologically.
*/ -}}
{{- define "kubernetes-nodes.defaultStorageClassName" -}}
{{- $classes := lookup "storage.k8s.io/v1" "StorageClass" "" "" -}}
{{- $name := "" -}}
{{- $createdAt := "" -}}
{{- range (dig "items" (list) ($classes | default dict)) -}}
{{-   $annotations := dig "metadata" "annotations" (dict) . -}}
{{-   if or (eq (dig "storageclass.kubernetes.io/is-default-class" "" $annotations | toString) "true") (eq (dig "storageclass.beta.kubernetes.io/is-default-class" "" $annotations | toString) "true") -}}
{{-     $at := dig "metadata" "creationTimestamp" "" . | toString -}}
{{-     if or (not $name) (gt $at $createdAt) -}}
{{-       $name = dig "metadata" "name" "" . | toString -}}
{{-       $createdAt = $at -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- $name -}}
{{- end -}}

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
