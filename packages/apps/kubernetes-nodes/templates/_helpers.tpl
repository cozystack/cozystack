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
Effective Talos `machine.kernel.modules` list for this worker pool.

Takes a context carrying .group and .groupName, returns a YAML list (empty
output when there is nothing to load, so the caller can gate the whole
`kernel:` block on it).

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

`NVreg_NvLinkDisable=1` on the `nvidia` module mirrors what the platform already
does for tenant clusters through the gpu-operator driver container, which
writes exactly that one line into the `nvidia-kernel-module-params` ConfigMap
(packages/system/gpu-operator, kernelModuleConfig.content). Cozystack passes
individual GPUs into worker VMs WITHOUT the NVSwitches, so the driver would
otherwise wait forever for an NVLink fabric that can never come up, leaving
Fabric State "In Progress" and failing every CUDA call with "system not yet
initialized". On Talos the driver comes from a system extension instead, and the
operator's driver container has to be turned off or the two clash. Once it is
off, nothing mounts that ConfigMap and the machine config is the only place left
to carry the parameter. A no-op on a PCIe card with no NVLink.

Module order is the order Talos' own NVIDIA documentation prescribes and the
order validated against a production GB202 passthrough node: `nvidia` first
(the others depend on it), then `nvidia_uvm` (CUDA unified memory, needed by
any workload using the CUDA runtime), then `nvidia_drm` and `nvidia_modeset`.
Talos loads them in list order, so this is not cosmetic.

This only covers loading a module the OS already carries. Which extension
supplies it is the pool's schematic (kubernetes-nodes.resolveOsImage) and is
not derivable from the pool — on Blackwell (GB202) specifically it has to be
the open-kernel-modules extension, since the proprietary one loads, creates
`/dev/nvidia0`, and then finds no devices.
*/}}
{{- define "kubernetes-nodes.kernelModules" -}}
{{- $group := .group -}}
{{- $groupName := .groupName -}}
{{- $modules := list -}}
{{- if kindIs "slice" $group.kernelModules -}}
{{- /* The emitted list is REBUILT from validated fields rather than passed through
         from the user's dict. Validating `.name` and `.parameters` and then emitting
         the raw item would let any other key ride along: `- {name: dummy, evil: "$(...)"}`
         reached the heredoc verbatim, because `toYaml` copies whatever is there and
         nothing upstream prunes it — `items` in values.schema.json carries no
         `additionalProperties: false`, and the aggregated apiserver wires that schema
         into defaulting only (pkg/registry/apps/application/rest_defaulting.go), with
         no pruning or validation on Create/Update. Allowlisting by construction closes
         that whole class instead of enumerating the fields it happens to know about.

         Why any of this is needed: the machine config is written through the reconcile
         Job's `cat <<EOF` heredoc with an UNQUOTED delimiter, which the script needs so
         ${RELEASE} and friends expand, and which therefore expands everything else in
         the block too. A module name of `nvidia$(id)` would run `id` inside the
         talos-reconcile pod, whose ServiceAccount can write TalosConfigTemplates and
         read Talos secrets. The values come from a tenant-facing CR, so this render is
         the last gate.

         Only the USER-SUPPLIED list is checked; the automatic NVIDIA set below is valid
         by construction. */}}
{{-   range $group.kernelModules -}}
{{-     $name := .name | default "" | toString -}}
{{-     if not (regexMatch `^[a-z0-9_-]+$` $name) -}}
{{-       fail (printf "nodeGroup %q: invalid kernelModules name %q — must be a kernel module name matching ^[a-z0-9_-]+$ (e.g. nvidia_uvm)" $groupName $name) -}}
{{-     end -}}
{{-     $params := list -}}
{{-     range .parameters | default list -}}
{{-       $param := . | toString -}}
{{- /* Two checks, because RE2's `\s` and `[:cntrl:]` are ASCII-only: a deny class
         built from them lets U+2028 and U+2029 through, `toYaml` writes either raw,
         and Helm's YAML parser reads it as a line break that ends the Job's
         `command` block scalar early. `[:graph:]` pins the parameter to printable
         ASCII, which closes every non-ASCII break at once; the second check then
         removes what the heredoc would expand. */ -}}
{{-       if not (and (regexMatch `^[[:graph:]]+$` $param) (regexMatch `^[^$\x60\\'\"]+$` $param)) -}}
{{-         fail (printf "nodeGroup %q: invalid kernelModules parameter %q on module %q — must be printable ASCII with no whitespace, and must not contain $, a backtick, a backslash or quotes (e.g. NVreg_EnableGpuFirmware=1, or the semicolon-separated NVreg_RegistryDwords=PowerMizerEnable=0x1;PerfLevelSrc=0x2222)" $groupName $param $name) -}}
{{-       end -}}
{{-       $params = append $params $param -}}
{{-     end -}}
{{-     $module := dict "name" $name -}}
{{-     if $params -}}
{{-       $_ := set $module "parameters" $params -}}
{{-     end -}}
{{-     $modules = append $modules $module -}}
{{-   end -}}
{{- else -}}
{{-   range $group.gpus | default list -}}
{{-     if hasPrefix "nvidia.com/" (.name | default "") -}}
{{-       $modules = list (dict "name" "nvidia" "parameters" (list "NVreg_NvLinkDisable=1")) (dict "name" "nvidia_uvm") (dict "name" "nvidia_drm") (dict "name" "nvidia_modeset") -}}
{{-     end -}}
{{-   end -}}
{{- end -}}
{{- if $modules -}}
{{ toYaml $modules }}
{{- end -}}
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
{{- /* Only the paths that build an HTTP source URL are held to this. A builtin
       pool clones a golden PVC: its DataVolume carries source.pvc and no
       source.http at all, so imageFactoryURL is never interpolated into
       anything it renders. Validating it there refused a value the pool does
       not consume, and the operator it refused is the one the feature is for --
       somebody moving to osImage.builtin to stop depending on the Factory is
       exactly the person likely to blank imageFactoryURL, and the message they
       got named a source URL this path never builds. The field is a bare string
       in values.schema.json and in the cozyrds openAPISchema, so an empty or
       malformed one reaches the render rather than being rejected at admission.
       The check itself stays exactly as strict for osImage.factory and for the
       no-osImage default, which are the two arms that do interpolate it. */ -}}
{{- if not $clone -}}
{{-   if not (regexMatch "^https?://[A-Za-z0-9._~:/?#\\[\\]@!&'()*+,;=%-]+$" ($factoryURL | toString)) -}}
{{-     fail (printf "nodeGroup %q: imageFactoryURL %q is not a plain http(s) URL. It is interpolated into the worker DataVolume source URL, so it must carry no whitespace, backtick, dollar sign or YAML metacharacters." .groupName ($factoryURL | toString)) -}}
{{-   end -}}
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
is the transfer the clone path exists to remove.

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

{{- /*
  Nameservers are addresses, so each entry is checked to be one.

  The reason it is checked rather than escaped: this value is written into the
  reconcile Job's unquoted heredoc, where the shell expands what it finds, and
  `quote` there is YAML quoting that does nothing about $(...) or backticks. The
  other tenant-controlled values that reach the same heredoc — talos.version,
  talos.schematicID, talos.installerRepository, talos.registryMirrors — are
  shell-escaped instead, as the INVARIANT comment beside them says. An address
  admits neither approach's failure mode: it has no metacharacters to escape.
  Checking also catches the ordinary mistake of writing a hostname where Talos
  takes only an IP.

  IPv6 is matched on its character set rather than its full grammar. What this
  guard owes is a bound on which bytes may reach the shell, not a verdict on
  whether every zero-run is written correctly, and the apiserver rejects a
  malformed address anyway.
*/ -}}
{{- define "kubernetes-nodes.assertDnsServersAreAddresses" -}}
{{- $v4 := `^((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])$` -}}
{{- range $i, $e := (default (list) .Values.proxmox.dnsServers) -}}
{{- $s := $e | toString -}}
{{- if not (or (regexMatch $v4 $s) (and (contains ":" $s) (regexMatch `^[0-9A-Fa-f:]+$` $s))) -}}
{{- fail (printf "proxmox.dnsServers[%d] is %q: entries must be IPv4 or IPv6 addresses. Talos takes addresses here, and this list is written into a shell heredoc, so anything else is both invalid for Talos and unsafe to interpolate." $i $s) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
  Two ways a pool can end up on the wrong substrate, both silent, both guarded
  here against the live cluster rather than against the `## @immutable` marker,
  which is dashboard-only.

  Switching an existing pool rolls every worker to the other hypervisor and, on
  the way, drops the machine template the draining MachineSet still references:
  the retention loop in nodegroup.yaml looks up only the kind the new substrate
  uses, so the old kind stops being rendered and Helm deletes it — exactly the
  failure that loop exists to prevent.

  A pool that disagrees with its parent is the other one. The README says the
  two must match; nothing checked it. A proxmox pool under a KubeVirt cluster
  gets machines no ProxmoxCluster owns, and its workers never join.

  Inert offline, like assertNoForeignPool and assertParentVersion above.
*/ -}}
{{- define "kubernetes-nodes.assertSubstrateMatches" -}}
{{- $clusterName := include "kubernetes-nodes.clusterName" . -}}
{{- $groupName := include "kubernetes-nodes.groupName" . -}}
{{- $isProxmox := eq (.Values.substrate | default "kubevirt") "proxmox" -}}
{{- $wantTemplateKind := ternary "ProxmoxMachineTemplate" "KubevirtMachineTemplate" $isProxmox -}}
{{- $wantClusterKind := ternary "ProxmoxCluster" "KubevirtCluster" $isProxmox -}}
{{- $md := lookup "cluster.x-k8s.io/v1beta1" "MachineDeployment" .Release.Namespace (printf "%s-%s" $clusterName $groupName) -}}
{{- if $md -}}
{{- $liveKind := dig "spec" "template" "spec" "infrastructureRef" "kind" "" $md -}}
{{- if and $liveKind (ne $liveKind $wantTemplateKind) -}}
{{- fail (printf "kubernetes-nodes: pool %q already runs %s workers and substrate is now %q, which renders a %s. Switching rolls every worker to the other hypervisor and drops the template the draining MachineSet still references. Create a new pool on the other substrate and scale this one down instead." $groupName $liveKind (.Values.substrate | default "kubevirt") $wantTemplateKind) -}}
{{- end -}}
{{- end -}}
{{- $cluster := lookup "cluster.x-k8s.io/v1beta1" "Cluster" .Release.Namespace $clusterName -}}
{{- if $cluster -}}
{{- $parentKind := dig "spec" "infrastructureRef" "kind" "" $cluster -}}
{{- if and $parentKind (ne $parentKind $wantClusterKind) -}}
{{- fail (printf "kubernetes-nodes: pool %q is set to substrate %q but its parent cluster %q runs on %s. A pool's substrate must match its cluster's: its machines would be owned by no infrastructure cluster and its workers would never join." $groupName (.Values.substrate | default "kubevirt") $clusterName $parentKind) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
This pool's node group — the SINGLE assembly of the dict that feeds every
consumer in this chart:

  * nodegroup.yaml, which renders the KubevirtMachineTemplate and the
    MachineDeployment (and hashes the former into its name);
  * talos-reconcile-job.yaml, which renders the TalosConfigTemplate spec and
    hashes it into the name the Job applies it under.

Emitted as YAML and read back with `fromYaml` at each call site. A Helm
template cannot return a dict and a Helm variable cannot cross a file
boundary, so this round-trip is what lets one assembly serve two files.

Why it has to be one assembly. The TalosConfigTemplate name is a hash of the
spec this dict renders, and two templates have to agree on that name: the Job
creates the object, the MachineDeployment's bootstrap.configRef points at it.
Assembling the dict twice makes them agree only by coincidence of the keys the
spec helper happens to read — drop a key on one side (`gpus`, say) and the two
names diverge, CAPI blocks on "templates do not exist" forever and no worker is
ever created. Sharing the *rendering* is not enough; the input has to be shared
too. Every value the pool exposes belongs here, or the consumer that reads
it hashes a different group than the one that does not.

kernelModules carries no entry in values.yaml, so the key is absent unless the
operator set it, and `dict` keeps it nil. It survives the round-trip as null,
which kubernetes-nodes.kernelModules reads as "unset", as opposed to an explicit
empty list that opts the pool out. Defaulting it to `list` here would collapse
the two.
*/}}
{{- define "kubernetes-nodes.group" -}}
{{- dict
      "minReplicas" .Values.minReplicas
      "maxReplicas" .Values.maxReplicas
      "instanceType" .Values.instanceType
      "diskSize" .Values.diskSize
      "storageClass" .Values.storageClass
      "osImage" .Values.osImage
      "roles" .Values.roles
      "resources" .Values.resources
      "gpus" .Values.gpus
      "kubelet" .Values.kubelet
      "logSerialConsole" .Values.logSerialConsole
      "podCpuLimit" .Values.podCpuLimit
      "podCpuRequest" .Values.podCpuRequest
      "kernelModules" .Values.kernelModules
      "proxmox" .Values.proxmox
    | toYaml -}}
{{- end -}}
