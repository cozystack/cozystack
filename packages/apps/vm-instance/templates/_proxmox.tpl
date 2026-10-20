{{- /*
  substrate decides what the machine is: a KubeVirt VirtualMachine (a pod)
  on kubevirt, a kubemox VirtualMachine on an external Proxmox cluster on
  proxmox. Safe to choose only once: switching would render a second
  machine under the same name on the other hypervisor while the first keeps
  running. The `## @immutable` marker is dashboard-only, which is why this
  reads the live object of the other substrate and refuses when it exists.
  Each lookup is gated on its API being served (a lookup of a kind the
  cluster does not know fails the render), and inert offline like the other
  lookup guards in this chart.
*/}}
{{- define "virtual-machine.substrate" -}}
{{- $substrate := .Values.substrate | default "kubevirt" -}}
{{- if and (ne $substrate "kubevirt") (ne $substrate "proxmox") -}}
{{- fail (printf "substrate must be \"kubevirt\" or \"proxmox\", got %q" $substrate) -}}
{{- end -}}
{{- $name := include "virtual-machine.fullname" . -}}
{{- if eq $substrate "proxmox" -}}
{{- if and (.Capabilities.APIVersions.Has "kubevirt.io/v1/VirtualMachine") (lookup "kubevirt.io/v1" "VirtualMachine" .Release.Namespace $name) -}}
{{- fail (printf "vm-instance: %s already runs on KubeVirt (substrate kubevirt) and substrate is now \"proxmox\". A machine cannot move between hypervisors; create a new instance on the other substrate." $name) -}}
{{- end -}}
{{- else -}}
{{- if and (.Capabilities.APIVersions.Has "proxmox.alperen.cloud/v1alpha1/VirtualMachine") (lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachine" .Release.Namespace $name) -}}
{{- fail (printf "vm-instance: %s already runs on Proxmox (substrate proxmox) and substrate is now %q. A machine cannot move between hypervisors; create a new instance on the other substrate." $name $substrate) -}}
{{- end -}}
{{- end -}}
{{- $substrate -}}
{{- end -}}

{{- /* The platform's Proxmox catalog, as JSON; see the same helper in vm-disk. */}}
{{- define "virtual-machine.proxmoxCatalog" -}}
{{- $cm := lookup "v1" "ConfigMap" "cozy-public" "proxmox-vm-catalog" -}}
{{- if not $cm -}}
{{- fail "substrate proxmox needs the platform's proxmox-vm package: the catalog ConfigMap cozy-public/proxmox-vm-catalog does not exist. Enable cozystack.proxmox-vm in bundles.enabledPackages and fill its catalog." -}}
{{- end -}}
{{- $catalog := dict -}}
{{- range $key, $value := $cm.data -}}
{{- if or (eq $key "connection") (eq $key "defaultNode") -}}
{{- $_ := set $catalog $key $value -}}
{{- else if or (eq $key "allowedNodes") (eq $key "dnsServers") -}}
{{- /* fromYaml only yields a map; a YAML list needs fromYamlArray. */ -}}
{{- $_ := set $catalog $key ($value | fromYamlArray) -}}
{{- else -}}
{{- $_ := set $catalog $key ($value | fromYaml) -}}
{{- end -}}
{{- end -}}
{{- $catalog | toJson -}}
{{- end -}}

{{- /*
  The sizing of the machine on Proxmox, as JSON {cores, sockets, memoryMiB,
  extra: {qm option: value}}: from `resources` when it sizes the VM in full
  (the same whole-number rules as on kubevirt, through domainResources),
  otherwise from the catalog's entry for `instanceType`. The RT series is not
  in the catalog on purpose; the failure text says so.
*/}}
{{- define "virtual-machine.proxmoxSizing" -}}
{{- $catalog := index . 0 -}}
{{- $root := index . 1 -}}
{{- $domain := include "virtual-machine.domainResources" $root | fromJson -}}
{{- $cores := dig "cpu" "cores" 0 $domain -}}
{{- $sockets := dig "cpu" "sockets" 0 $domain -}}
{{- $memory := dig "resources" "requests" "memory" "" $domain -}}
{{- $sizing := dict -}}
{{- if and $cores $sockets $memory -}}
{{- $bytes := include "cozy-lib.resources.toFloat" $memory | float64 -}}
{{- $sizing = dict "cores" (int $cores) "sockets" (int $sockets) "memoryMiB" (divf $bytes 1048576.0 | ceil | int) "extra" (dict) -}}
{{- else -}}
{{- $type := include "virtual-machine.effectiveInstanceType" $root -}}
{{- if not $type -}}
{{- fail "Either instanceType or resources (cpu, sockets, memory) must be specified" -}}
{{- end -}}
{{- $types := $catalog.instancetypes | default dict -}}
{{- $entry := index $types $type -}}
{{- if not $entry -}}
{{- if hasPrefix "rt1." $type -}}
{{- fail (printf "instanceType %q is a realtime type, which the proxmox substrate does not offer: Proxmox gives no realtime guarantee. Pick a u1, o1, d1, m1 or cx1 type, or size the VM with resources." $type) -}}
{{- end -}}
{{- fail (printf "instanceType %q is not in the platform's Proxmox catalog (known: %s)" $type (join ", " (keys $types | sortAlpha))) -}}
{{- end -}}
{{- $extra := dict -}}
{{- range $k, $v := $entry -}}
{{- if not (has $k (list "cores" "sockets" "memoryMiB")) -}}
{{- $_ := set $extra $k $v -}}
{{- end -}}
{{- end -}}
{{- $sizing = dict "cores" (int $entry.cores) "sockets" (int ($entry.sockets | default 1)) "memoryMiB" (int $entry.memoryMiB) "extra" $extra -}}
{{- end -}}
{{- $sizing | toJson -}}
{{- end -}}

{{- /* The catalog's profile for `instanceProfile`, as JSON. */}}
{{- define "virtual-machine.proxmoxProfile" -}}
{{- $catalog := index . 0 -}}
{{- $root := index . 1 -}}
{{- $profiles := $catalog.profiles | default dict -}}
{{- $name := $root.Values.instanceProfile | default "" -}}
{{- $profile := dict "ostype" "l26" "bios" "seabios" "machine" "q35" "scsiController" "virtio-scsi-single" -}}
{{- if $name -}}
{{- $profile = index $profiles $name -}}
{{- if not $profile -}}
{{- fail (printf "instanceProfile %q is not in the platform's Proxmox catalog (known: %s)" $name (join ", " (keys $profiles | sortAlpha))) -}}
{{- end -}}
{{- end -}}
{{- $profile | toJson -}}
{{- end -}}

{{- /*
  The disks of the machine on Proxmox, as JSON: one entry per `disks[]` with
  the device it gets and the VirtualMachineVolume it attaches, plus the node
  every disk lives on. The volumes are the vm-disk objects of this substrate;
  a disk that is a DataVolume (substrate kubevirt) or does not exist fails
  the render with that fact, since the machine could not boot from it.
*/}}
{{- define "virtual-machine.proxmoxDisks" -}}
{{- $root := . -}}
{{- $disks := list -}}
{{- $node := "" -}}
{{- $counters := dict "scsi" 0 "virtio" 0 "sata" 0 "ide" 0 -}}
{{- range $i, $disk := $root.Values.disks -}}
{{- $volName := printf "vm-disk-%s" $disk.name -}}
{{- $vol := lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachineVolume" $root.Release.Namespace $volName -}}
{{- if not $vol -}}
{{- if and ($root.Capabilities.APIVersions.Has "cdi.kubevirt.io/v1beta1/DataVolume") (lookup "cdi.kubevirt.io/v1beta1" "DataVolume" $root.Release.Namespace $volName) -}}
{{- fail (printf "disk %q is a KubeVirt disk (substrate kubevirt); a machine on the proxmox substrate can only attach vm-disk objects of the same substrate" $disk.name) -}}
{{- end -}}
{{- fail (printf "Specified disk not exists in cluster: %s" $disk.name) -}}
{{- end -}}
{{- $volNode := dig "spec" "nodeName" "" $vol -}}
{{- if and $node (ne $node $volNode) -}}
{{- fail (printf "disk %q lives on Proxmox node %q while an earlier disk lives on %q; a machine can only attach disks of one node" $disk.name $volNode $node) -}}
{{- end -}}
{{- $node = $volNode -}}
{{- $bus := $disk.bus | default "scsi" -}}
{{- if not (hasKey $counters $bus) -}}
{{- fail (printf "disk %q: bus %q is not one of scsi, virtio, sata, ide" $disk.name $bus) -}}
{{- end -}}
{{- $n := index $counters $bus -}}
{{- $_ := set $counters $bus (add $n 1) -}}
{{- $disks = append $disks (dict "device" (printf "%s%d" $bus $n) "volume" $volName "name" $disk.name) -}}
{{- end -}}
{{- dict "disks" $disks "node" $node | toJson -}}
{{- end -}}

{{- /*
  The SMBIOS UUID of the machine: the same hash of namespace, name and
  cloudInitSeed the KubeVirt path uses, so a seed change re-runs cloud-init
  on both substrates. With no seed, the UUID the existing kubemox machine
  already carries wins, so that dropping the seed does not re-run cloud-init
  either. The lookup is gated on the kubemox API being served and is inert
  offline, like the KubeVirt helper it mirrors.
*/}}
{{- define "virtual-machine.proxmoxStableUuid" -}}
{{- $name := include "virtual-machine.fullname" . -}}
{{- $hash := sha256sum (printf "%s-%s-%s" .Release.Namespace $name .Values.cloudInitSeed) -}}
{{- $uuid := printf "%s-%s-4%s-9%s-%s" (substr 0 8 $hash) (substr 8 12 $hash) (substr 13 16 $hash) (substr 17 20 $hash) (substr 20 32 $hash) -}}
{{- if and (eq .Values.cloudInitSeed "") (.Capabilities.APIVersions.Has "proxmox.alperen.cloud/v1alpha1/VirtualMachine") -}}
{{- with lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachine" .Release.Namespace $name -}}
{{- with dig "spec" "vmSpec" "smbiosUUID" "" . -}}
{{- $uuid = . -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $uuid -}}
{{- end -}}
