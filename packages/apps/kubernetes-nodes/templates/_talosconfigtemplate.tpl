{{- /*
  Talos worker machine config for one node group, rendered as the `spec:`
  block of a TalosConfigTemplate.

  Single source of truth for that spec, shared by the two consumers that have
  to agree on it byte-for-byte:

    * the talos-reconcile Job, which embeds it in the heredoc it
      `kubectl apply`s once the runtime inputs (apiserver Service ClusterIP,
      Talos CA, tenant Kubernetes CA, management CoreDNS ClusterIP) exist;
    * the MachineDeployment, which points
      spec.template.spec.bootstrap.configRef at the TalosConfigTemplate named
      after a hash of this spec.

  Why the hash. TalosConfigTemplate.spec is immutable — the
  vtalosconfigtemplate.cluster.x-k8s.io webhook rejects any mutation with
  "TalosConfigTemplate.Spec is immutable" — so the object can never be updated
  in place. Applying it under a fixed name means that once it exists, no change
  to the rendered machine config can ever reach the cluster: the apply is
  denied server-side and the stale spec stays (cozystack/cozystack#3515,
  observed with machine.network.searchDomains after a clusterDomain
  correction; the same holds for certSANs, the registry mirrors, nameservers and
  any future field). Naming the template after a content hash — the idiom
  KubevirtMachineTemplate already uses in this chart — turns a content change
  into a NEW template instead of a rejected mutation, and CAPI propagates it
  the way it is meant to: a new MachineSet rolls the workers onto the new
  bootstrap config.

  The `${...}` placeholders are shell variables the Job expands at apply time,
  and are deliberately NOT part of the hash: the CA certificates, the Service
  ClusterIP, the CoreDNS ClusterIP and the Talos/bootstrap tokens are runtime
  facts of an already-provisioned cluster rather than chart inputs. Hashing
  them would roll every worker on a certificate rotation or a Service
  re-address.

  Input: a dict with `root` (the chart root context) and `group` (the node
  group's values — instanceType / resources / gpus / kubelet / osImage /
  kernelModules). Everything else
  is derived here rather than passed in, so the two call sites cannot drift
  apart and compute different hashes for the same node group.
*/}}
{{- define "kubernetes.talosConfigTemplateSpec" -}}
{{- $root := .root }}
{{- $group := .group }}
{{- $kubeletVersion := include "kubernetes.versionMap" $root | trim }}
{{- /* Resolve this pool's worker image (schematicID, version) from .Values.osImage
       — the same selection templates/nodegroup.yaml uses for the boot disk — so the
       in-guest Talos installer image matches the OS this pool actually booted.
       Defaults to the pool's talos.*; a pool with osImage.builtin / osImage.factory
       pins its own flavor and version so an in-guest upgrade does not flip it back to
       the pool default. */}}
{{- /* Shared with nodegroup.yaml's boot-disk source rather than mirrored here:
       a disk booted from one Talos flavor and an installer pinned to another is
       the silently-broken pairing this chart's support matrix was factored out
       to prevent, and two hand-kept copies of the resolution are how the two
       drift apart. The helper also carries the format checks, so this file no
       longer depends on the other one aborting the shared render first. */}}
{{- $resolvedImage := dict }}
{{- include "kubernetes-nodes.resolveOsImage" (dict
      "osImage" $group.osImage
      "talos" $root.Values.talos
      "out" $resolvedImage
      "groupName" (include "kubernetes-nodes.groupName" $root)) }}
{{- $talosSchematicID := $resolvedImage.schematicID }}
{{- $talosVersion := $resolvedImage.version }}
{{- $podCIDR := "10.243.0.0/16" }}
{{- $serviceCIDR := "10.95.0.0/16" }}
{{- $dnsDomain := include "kubernetes.tenantClusterDomain" $root }}
{{- /* Management cluster DNS domain. Tenant Kubernetes workers run as
       KubeVirt VMs inside the management cluster's pod network and need
       to resolve management-cluster service short names ("<svc>.<ns>.svc")
       the same way an Ubuntu+kubeadm worker did via cloud-init DHCP. The
       kubevirt-csi NFS mount path is the load-bearing case: kubelet on
       the worker host calls `mount.nfs linstor-csi-nfs.cozy-linstor.svc`
       (no domain suffix), and without searchDomains in /etc/resolv.conf
       the host resolver hands that name to management CoreDNS verbatim
       and gets NXDOMAIN.

       Distinct from tenantClusterDomain ("cluster.local"), which is the
       *tenant* kubelet --cluster-dns zone. This value is the
       *management* cluster-domain, sourced from .Values._cluster
       (populated by cozystack-controller from the platform config). */}}
{{- $mgmtClusterDomain := (index ($root.Values._cluster | default dict) "cluster-domain") | default "cozy.local" }}
{{- /* Kubelet reservations, mirroring the computation the MachineDeployment
       side validates: auto-computed system/kube reserved memory = 5% of
       effective memory clamped to [256Mi, 1Gi]; auto-computed system/kube
       reserved CPU = 5% of effective CPU clamped to [50m, 500m].
       instanceType lookup is optional — operators that override
       resources.cpu / resources.memory on the nodeGroup get those, otherwise
       the chart falls back to the same static-defaults pair the
       MachineDeployment validator uses on a no-instanceType setup. */}}
{{- $instanceType := dict }}
{{- if $group.instanceType }}
{{-   $instanceType = lookup "instancetype.kubevirt.io/v1beta1" "VirtualMachineClusterInstancetype" "" $group.instanceType }}
{{-   $instanceType = $instanceType | default dict }}
{{- end }}
{{- $effectiveMemory := "" }}
{{- if and $group.resources $group.resources.memory }}
{{-   $effectiveMemory = $group.resources.memory | toString }}
{{- else if and $instanceType $instanceType.spec $instanceType.spec.memory $instanceType.spec.memory.guest }}
{{-   $effectiveMemory = $instanceType.spec.memory.guest | toString }}
{{- end }}
{{- $effectiveCpu := "" }}
{{- if and $group.resources $group.resources.cpu }}
{{-   $effectiveCpu = $group.resources.cpu | toString }}
{{- else if and $instanceType $instanceType.spec $instanceType.spec.cpu $instanceType.spec.cpu.guest }}
{{-   $effectiveCpu = $instanceType.spec.cpu.guest | toString }}
{{- end }}
{{- $autoReservedMi := 256 }}
{{- if $effectiveMemory }}
{{-   $effectiveMemMi := divf (include "cozy-lib.resources.toFloat" $effectiveMemory | float64) 1048576.0 | int }}
{{-   $fivePercentMi := mulf ($effectiveMemMi | float64) 0.05 | int }}
{{-   $autoReservedMi = min (max $fivePercentMi 256) 1024 }}
{{- end }}
{{- $autoReservedMillicores := 50 }}
{{- if $effectiveCpu }}
{{-   $effectiveCpuMillicores := include "kubernetes.cpuToMillicores" $effectiveCpu | int }}
{{-   $fivePercentMillis := mulf ($effectiveCpuMillicores | float64) 0.05 | int }}
{{-   $autoReservedMillicores = min (max $fivePercentMillis 50) 500 }}
{{- end }}
{{- $kubeletOverride := $group.kubelet | default dict }}
{{- $systemReservedMemory := $kubeletOverride.systemReservedMemory | default (printf "%dMi" $autoReservedMi) }}
{{- $kubeReservedMemory := $kubeletOverride.kubeReservedMemory | default (printf "%dMi" $autoReservedMi) }}
{{- $systemReservedCpu := $kubeletOverride.systemReservedCpu | default (printf "%dm" $autoReservedMillicores) }}
{{- $kubeReservedCpu := $kubeletOverride.kubeReservedCpu | default (printf "%dm" $autoReservedMillicores) }}
{{- $evictionHardMemory := $kubeletOverride.evictionHardMemory | default "7%" }}
{{- $evictionSoftMemory := $kubeletOverride.evictionSoftMemory | default "10%" -}}
{{- /* Registry mirrors and their TLS settings. A pool's own talos.registryMirrors
       replaces the platform map as a whole, so a pool that sets one gets exactly
       what it wrote; the platform's TLS settings apply either way, since they are
       keyed by mirror endpoint and an entry for a host no mirror uses does nothing.
       Both reach Talos as separate documents appended to the machine config, which
       the deprecated machine.registries section replaced in Talos 1.12; the section
       is not rendered at all. Talos below 1.12 would reject those documents and the
       node would never take its config, so the render refuses that pairing instead. */}}
{{- $clusterRegistries := (index ($root.Values._cluster | default dict) "kubernetes-registries") | default dict }}
{{- $registryMirrors := $root.Values.talos.registryMirrors | default $clusterRegistries.mirrors | default dict }}
{{- $registryConfig := $clusterRegistries.config | default dict }}
{{- $registryDocuments := "" }}
{{- if or $registryMirrors $registryConfig }}
{{-   if not (semverCompare ">=1.12.0-0" $talosVersion) }}
{{-     fail (printf "nodeGroup %q: registry mirrors and registry TLS settings are rendered as RegistryMirrorConfig and RegistryTLSConfig documents, which Talos has only since 1.12, and this pool resolves to Talos %s. Move the pool to Talos 1.12 or newer, or clear talos.registryMirrors and the platform kubernetesRegistries." (include "kubernetes-nodes.groupName" $root) $talosVersion) }}
{{-   end }}
{{-   $registryDocuments = include "kubernetes.talosRegistryDocuments" (dict "mirrors" $registryMirrors "config" $registryConfig) }}
{{- end }}
{{- /* The spec proper. Everything above only computes inputs; nothing above
       this line may emit output, or the leading newline lands in the caller's
       heredoc and in the hash. */ -}}
spec:
  template:
    spec:
      generateType: none
      talosVersion: "{{ $talosVersion | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}"
      {{- /* INVARIANT (cozystack/cozystack#3513): the data block below is emitted from the unquoted `TCT_MANIFEST="$(cat <<EOF` heredoc in the talos-reconcile Job, so it is subject to shell parameter expansion and command substitution at Job runtime. Every tenant-controlled free-form value interpolated into it must therefore be either shell-escaped for backslash, dollar and backtick (as talosVersion here and the registry documents at the end are) or render-time pattern-validated to exclude those bytes (as the kubelet reservation fields are in nodegroup.yaml). A new such field added here without one of the two silently reopens command injection into a host-cluster Pod. Note: an asterisk-slash sequence inside this Helm comment closes it early and breaks the render, so the kubelet field names are spelled out here rather than written as globs. This is a Helm comment: it is stripped at render, so its own text never reaches the heredoc. */}}
      data: |
        version: v1alpha1
        persist: true
        machine:
          type: worker
          token: ${TALOS_TOKEN}
          network:
            # Management CoreDNS as the worker's host
            # resolver, plus management cluster search
            # domains so partial names like
            # "linstor-csi-nfs.cozy-linstor.svc" resolve
            # the same way they do on Ubuntu+kubeadm
            # workers in main (which got these via
            # cloud-init/DHCP). Without searchDomains the
            # kubevirt-csi NFS mount fails NXDOMAIN
            # because mount.nfs hands the partial name
            # to the resolver verbatim and management
            # CoreDNS only knows full FQDNs.
            #
            # Tenant in-cluster pods are not affected:
            # dnsPolicy: ClusterFirst routes them through
            # kubelet --cluster-dns (tenant CoreDNS).
            # This block only configures host-side
            # resolution (kubelet mount syscalls, image
            # pulls, pods opted out of cluster DNS).
            nameservers:
              {{- /* A kubevirt worker resolves through the management
                    cluster's CoreDNS over the pod network. A proxmox
                    worker is off-cluster and cannot reach that
                    ClusterIP any more than it can reach the
                    apiserver's, so it carries its own resolvers. */}}
              {{- if eq ($root.Values.substrate | default "kubevirt") "proxmox" }}
              {{- range $root.Values.proxmox.dnsServers }}
              - {{ . | quote }}
              {{- end }}
              {{- else }}
              - ${COREDNS_IP}
              {{- end }}
            searchDomains:
              - svc.{{ $mgmtClusterDomain }}
              - {{ $mgmtClusterDomain }}
              {{- if ne $mgmtClusterDomain "cluster.local" }}
              - svc.cluster.local
              - cluster.local
              {{- end }}
            extraHostEntries:
              - ip: ${SVC_IP}
                aliases:
                  - ${RELEASE}.${NS}.svc
                  - ${RELEASE}.${NS}.svc.{{ $dnsDomain }}
          ca:
            crt: ${TALOS_CA_B64}
          {{- if $group.gpus }}
          # GPU node-groups: label every node `gpu=on` so
          # HAMi's hami-device-plugin DaemonSet (nodeSelector
          # gpu=on) schedules and advertises nvidia.com/gpu.
          # Without the label the plugin stays at DESIRED=0
          # and no GPUs are exposed to the tenant. Mirrors
          # main's kubeadm config (kubeletExtraArgs node-
          # labels: "gpu=on") so HAMi behaviour is identical
          # before and after the Talos worker rollover.
          nodeLabels:
            gpu: "on"
          {{- end }}
          {{- /* Emitted only when the pool resolves to a non-empty list, so a
                 pool without modules renders the machine config it rendered
                 before this field existed. */}}
          {{- $kernelModules := include "kubernetes-nodes.kernelModules" (dict "group" $group "groupName" (include "kubernetes-nodes.groupName" $root)) }}
          {{- if $kernelModules }}
          kernel:
            modules:
              {{- $kernelModules | nindent 14 }}
          {{- end }}
          kubelet:
            image: ghcr.io/siderolabs/kubelet:{{ $kubeletVersion }}
            {{- /* Without this the kubelet never applies the
                  node.cloudprovider.kubernetes.io/uninitialized
                  taint, the Proxmox cloud-controller-manager
                  never gets to initialise the Node, and the
                  topology.kubernetes.io/{region,zone} labels are
                  never set. The CSI node plugin then exits
                  fatally on "Failed to get region or zone for
                  node", so the tenant ends up with a
                  StorageClass and no way to attach a volume.
                  The kubevirt path has no cloud provider and
                  must not carry the flag: it would taint every
                  worker with nothing to remove the taint. */}}
            {{- if eq ($root.Values.substrate | default "kubevirt") "proxmox" }}
            extraArgs:
              cloud-provider: external
            {{- end }}
            extraConfig:
              systemReserved:
                cpu: "{{ $systemReservedCpu }}"
                memory: "{{ $systemReservedMemory }}"
              kubeReserved:
                cpu: "{{ $kubeReservedCpu }}"
                memory: "{{ $kubeReservedMemory }}"
              evictionHard:
                memory.available: "{{ $evictionHardMemory }}"
                nodefs.available: "10%"
                imagefs.available: "15%"
                nodefs.inodesFree: "5%"
              evictionSoft:
                memory.available: "{{ $evictionSoftMemory }}"
                nodefs.available: "15%"
                imagefs.available: "20%"
              evictionSoftGracePeriod:
                memory.available: "1m30s"
                nodefs.available: "1m30s"
                imagefs.available: "1m30s"
              evictionMinimumReclaim:
                memory.available: "256Mi"
          install:
            disk: /dev/vda
            image: {{ $root.Values.talos.installerRepository | trimSuffix "/" | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}/{{ $talosSchematicID | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}:{{ $talosVersion | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}
            wipe: false
          features:
            rbac: true
            kubePrism:
              enabled: false
        cluster:
          id: ${CLUSTER_ID}
          secret: ${CLUSTER_SECRET}
          controlPlane:
            endpoint: https://${RELEASE}.${NS}.svc:6443
          clusterName: ${RELEASE}
          network:
            dnsDomain: {{ $dnsDomain }}
            podSubnets:
              - {{ $podCIDR }}
            serviceSubnets:
              - {{ $serviceCIDR }}
          token: ${BOOTSTRAP_TOKEN}
          ca:
            crt: ${K8S_CA_B64}
          discovery:
            enabled: true
            registries:
              kubernetes:
                disabled: true
              service:
                disabled: true
        {{- with $registryDocuments }}
        ---
        {{- . | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" | nindent 8 }}
        {{- end }}
{{- end }}

{{- /*
  Content hash of the TalosConfigTemplate spec, truncated the same way the
  KubevirtMachineTemplate hash in this chart is. Takes the same dict as
  kubernetes.talosConfigTemplateSpec, so the Job that creates the template and
  the MachineDeployment that references it always agree on the name.
*/}}
{{- define "kubernetes.talosConfigTemplateHash" -}}
{{- include "kubernetes.talosConfigTemplateSpec" . | sha256sum | trunc 6 -}}
{{- end }}

{{- /*
  The Talos documents that carry registry mirrors and their TLS settings, joined
  by document separators and not indented, escaped or prefixed: the caller owns
  those, because they depend on where the text is embedded.

  Input: `mirrors` (registry host to {endpoints, skipFallback, overridePath}) and
  `config` (mirror endpoint host to {tls: {insecureSkipVerify, ca}}). The mirror
  shape is the one talos.registryMirrors has always had; each endpoint becomes
  {url, overridePath}, with the host's overridePath on every one of them.

  Only tls.insecureSkipVerify and tls.ca are read from config. Credentials and
  client keys are left out on purpose: this text is stored in the TalosConfigTemplate
  and the reconcile Job in the tenant namespace, so whatever is rendered here is
  readable there.

  The three switches (overridePath, skipFallback, insecureSkipVerify) are on only for
  a true value, and a quoted "false" is off, not on: read by truthiness it would be a
  non-empty string, and the one that skips TLS verification would turn it off for a
  value written to keep it on.
*/}}
{{- define "kubernetes.talosRegistryDocuments" -}}
{{- $docs := list }}
{{- range $host, $mirror := .mirrors }}
{{-   if not (kindIs "map" $mirror) }}
{{-     fail (printf "registry mirror %q must be a map with an endpoints list, got %v" $host $mirror) }}
{{-   end }}
{{-   range $key, $_ := $mirror }}
{{-     if not (has $key (list "endpoints" "skipFallback" "overridePath")) }}
{{-       fail (printf "registry mirror %q: %s is not supported, only endpoints, skipFallback and overridePath are read, and a misspelt switch would be dropped without a word" $host $key) }}
{{-     end }}
{{-   end }}
{{-   $endpoints := list }}
{{-   range $mirror.endpoints }}
{{-     if not (kindIs "string" .) }}
{{-       fail (printf "registry mirror %q: every endpoint must be a URL string, got %v" $host .) }}
{{-     end }}
{{-     $endpoint := dict "url" . }}
{{-     if eq (toString $mirror.overridePath) "true" }}
{{-       $_ := set $endpoint "overridePath" true }}
{{-     end }}
{{-     $endpoints = append $endpoints $endpoint }}
{{-   end }}
{{-   if not $endpoints }}
{{-     fail (printf "registry mirror %q has no endpoints" $host) }}
{{-   end }}
{{-   $doc := dict "apiVersion" "v1alpha1" "kind" "RegistryMirrorConfig" "name" $host "endpoints" $endpoints }}
{{-   if eq (toString $mirror.skipFallback) "true" }}
{{-     $_ := set $doc "skipFallback" true }}
{{-   end }}
{{-   $docs = append $docs ($doc | toYaml | trim) }}
{{- end }}
{{- range $endpoint, $entry := .config }}
{{-   $tls := dict }}
{{-   if kindIs "map" $entry }}
{{-     $tls = $entry.tls | default dict }}
{{-   end }}
{{-   $doc := dict "apiVersion" "v1alpha1" "kind" "RegistryTLSConfig" "name" $endpoint }}
{{-   if eq (toString $tls.insecureSkipVerify) "true" }}
{{-     $_ := set $doc "insecureSkipVerify" true }}
{{-   end }}
{{-   if $tls.ca }}
{{-     $_ := set $doc "ca" ($tls.ca | toString) }}
{{-   end }}
{{-   if gt (len $doc) 3 }}
{{-     $docs = append $docs ($doc | toYaml | trim) }}
{{-   end }}
{{- end }}
{{- join "\n---\n" $docs -}}
{{- end }}
