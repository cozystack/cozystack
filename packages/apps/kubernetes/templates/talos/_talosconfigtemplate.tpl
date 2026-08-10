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
  correction; the same holds for certSANs, machine.registries, nameservers and
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
  group's values — instanceType / resources / gpus / kubelet). Everything else
  is derived here rather than passed in, so the two call sites cannot drift
  apart and compute different hashes for the same node group.
*/}}
{{- define "kubernetes.talosConfigTemplateSpec" -}}
{{- $root := .root }}
{{- $group := .group }}
{{- $kubeletVersion := include "kubernetes.versionMap" $root | trim }}
{{- $talosVersion := $root.Values.talos.version }}
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
{{- /* The spec proper. Everything above only computes inputs; nothing above
       this line may emit output, or the leading newline lands in the caller's
       heredoc and in the hash. */ -}}
spec:
  template:
    spec:
      generateType: none
      talosVersion: "{{ $talosVersion | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}"
      {{- /* INVARIANT (cozystack/cozystack#3513): the data block below is emitted from the unquoted `cat <<EOF | kubectl apply` heredoc in the talos-reconcile Job's command, so it is subject to shell parameter expansion and command substitution at Job runtime. This spec is rendered here and nindented into that heredoc by the caller, so the invariant travels with the text rather than with the Job template. Every tenant-controlled free-form value interpolated into it must therefore be either shell-escaped for backslash, dollar and backtick (as talosVersion here and registryMirrors below are) or render-time pattern-validated to exclude those bytes (as the kubelet reservation fields are, in cluster.yaml in the kubernetes chart and in nodegroup.yaml in kubernetes-nodes; this file is byte-identical across the two, so it names both). A new such field added here without one of the two silently reopens command injection into a host-cluster Pod. Note: an asterisk-slash sequence inside this Helm comment closes it early and breaks the render, so the kubelet field names are spelled out here rather than written as globs. This is a Helm comment: it is stripped at render, so its own text never reaches the heredoc or the content hash. */}}
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
              - ${COREDNS_IP}
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
          kubelet:
            image: ghcr.io/siderolabs/kubelet:{{ $kubeletVersion }}
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
            image: {{ $root.Values.talos.installerRepository | trimSuffix "/" | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}/{{ $root.Values.talos.schematicID | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}:{{ $talosVersion | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" }}
            wipe: false
          features:
            rbac: true
            kubePrism:
              enabled: false
          {{- with $root.Values.talos.registryMirrors }}
          registries:
            mirrors:
              {{- /* registryMirrors is free-form tenant-facing input rendered into the unquoted reconcile-Job heredoc this spec is nindented into, so the value is escaped (backslash, dollar, backtick) to render as a literal and never be shell-expanded or command-substituted at Job runtime. This is a Helm comment: it is stripped at render, so its own text never reaches the heredoc. See cozystack/cozystack#3513. */}}
              {{- toYaml . | replace "\\" "\\\\" | replace "$" "\\$" | replace "`" "\\`" | nindent 14 }}
          {{- end }}
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
