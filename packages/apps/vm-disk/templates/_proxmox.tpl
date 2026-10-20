{{- /*
  substrate decides what a disk is: a CDI DataVolume on kubevirt, a Proxmox
  volume on a holder VM on proxmox. It is safe to choose only once: the two
  objects share nothing, so switching would render a new, empty disk under the
  same name while the old one still holds the data. The `## @immutable` marker
  is dashboard-only, which is why this reads the live object of the other
  substrate and refuses when it exists.

  Each lookup is gated on its API being served: a lookup of a kind the
  cluster does not know fails the render instead of returning nothing, and a
  Proxmox-only cluster has no CDI just as a KubeVirt-only one has no kubemox.
  Inert offline, like the other lookup guards in this chart: with no apiserver
  the gates are closed and the render proceeds, so `helm template` and the
  unit tests are unaffected and a first install has nothing to compare against.
*/}}
{{- define "vm-disk.substrate" -}}
{{- $substrate := .Values.substrate | default "kubevirt" -}}
{{- if and (ne $substrate "kubevirt") (ne $substrate "proxmox") -}}
{{- fail (printf "substrate must be \"kubevirt\" or \"proxmox\", got %q" $substrate) -}}
{{- end -}}
{{- if eq $substrate "proxmox" -}}
{{- if and (.Capabilities.APIVersions.Has "cdi.kubevirt.io/v1beta1/DataVolume") (lookup "cdi.kubevirt.io/v1beta1" "DataVolume" .Release.Namespace .Release.Name) -}}
{{- fail (printf "vm-disk: %s already exists as a DataVolume (substrate kubevirt) and substrate is now \"proxmox\". A disk cannot move between hypervisors; create a new disk on the other substrate and copy the data." .Release.Name) -}}
{{- end -}}
{{- else -}}
{{- if and (.Capabilities.APIVersions.Has "proxmox.alperen.cloud/v1alpha1/VirtualMachineVolume") (lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachineVolume" .Release.Namespace .Release.Name) -}}
{{- fail (printf "vm-disk: %s already exists as a VirtualMachineVolume (substrate proxmox) and substrate is now %q. A disk cannot move between hypervisors; create a new disk on the other substrate and copy the data." .Release.Name $substrate) -}}
{{- end -}}
{{- end -}}
{{- $substrate -}}
{{- end -}}

{{- /*
  The platform's Proxmox catalog: the ConfigMap the proxmox-vm package renders
  into cozy-public. Every tenant chart on this substrate reads it, so a
  platform without the package fails the render with that fact rather than
  with a kubemox object nothing reconciles. Returned as JSON for `fromJson`,
  since a define can only return a string.
*/}}
{{- define "vm-disk.proxmoxCatalog" -}}
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

{{- /* Whole GiB a disk quantity needs: 5Gi -> 5, 1500Mi -> 2, 10G -> 10. */}}
{{- define "vm-disk.gib" -}}
{{- $bytes := include "cozy-lib.resources.toFloat" . | float64 -}}
{{- if le $bytes 0.0 -}}
{{- fail (printf "storage (%s) must be a positive quantity" .) -}}
{{- end -}}
{{- divf $bytes 1073741824.0 | ceil | int -}}
{{- end -}}

{{- /*
  What `source.image.name` means on the proxmox substrate, as the JSON of the
  VirtualMachineVolume source to render. Two places can define an image:

  1. the catalog's `images` map: a Proxmox volume id the platform points at
     directly (a template's disk, or a file on an import storage), copied with
     import-from and never an object of its own;
  2. a VirtualMachineVolume in cozy-public labelled
     `vm-default-images.cozystack.io/name: <name>` and marked cloneable: what
     the vm-default-images package renders on this substrate, and what a
     platform operator publishes by hand to make a tenant's disk cloneable.
     Copied through source.disk, so kubemox checks it is Ready and free.

  The catalog wins when both name the same image: it is explicit platform
  configuration, the other is discovered. A published image is per node (a
  Proxmox volume lives on one node), so the match also needs the node of the
  disk being created; one on another node is reported as such rather than as
  missing. The lookup is gated on the API being served, like the substrate
  guards, and inert offline.

  Called as (list $catalog $root $name $node).
*/}}
{{- define "vm-disk.proxmoxImageSource" -}}
{{- $catalog := index . 0 -}}
{{- $root := index . 1 -}}
{{- $name := index . 2 -}}
{{- $node := index . 3 -}}
{{- $images := $catalog.images | default dict -}}
{{- $volid := index $images $name -}}
{{- if $volid -}}
{{- dict "volume" $volid | toJson -}}
{{- else -}}
{{- $match := dict -}}
{{- $elsewhere := list -}}
{{- if $root.Capabilities.APIVersions.Has "proxmox.alperen.cloud/v1alpha1/VirtualMachineVolume" -}}
{{- range (lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachineVolume" "cozy-public" "").items | default list -}}
{{- if eq (dig "metadata" "labels" "vm-default-images.cozystack.io/name" "" . | toString) $name -}}
{{- if eq (dig "spec" "nodeName" "" . | toString) $node -}}
{{- $match = . -}}
{{- else -}}
{{- $elsewhere = append $elsewhere (dig "spec" "nodeName" "" . | toString) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $match -}}
{{- if not (dig "spec" "cloneable" false $match) -}}
{{- fail (printf "image %q is published in cozy-public as VirtualMachineVolume %s, but that volume is not marked cloneable, so kubemox would not copy it into this namespace. Ask the platform administrator to set spec.cloneable: true on it." $name $match.metadata.name) -}}
{{- end -}}
{{- if eq (dig "status" "phase" "" $match | toString) "Failed" -}}
{{- fail (printf "image %q: VirtualMachineVolume %s in cozy-public is Failed, so a copy of it would never complete. Ask the platform administrator to check its Ready condition." $name $match.metadata.name) -}}
{{- end -}}
{{- dict "disk" (dict "name" $match.metadata.name "namespace" "cozy-public") | toJson -}}
{{- else if $elsewhere -}}
{{- fail (printf "image %q is published in cozy-public on node %s, and this disk is created on node %s. A Proxmox volume can only be copied on its own node: set proxmox.node to one of those, or ask the platform administrator to publish the image on %s." $name (join ", " ($elsewhere | uniq | sortAlpha)) $node $node) -}}
{{- else -}}
{{- fail (printf "image %q is not in the platform's Proxmox catalog (known: %s) and no cloneable image of that name is published in cozy-public. On this substrate an image is a Proxmox volume the platform lists in its catalog, or a VirtualMachineVolume in cozy-public labelled vm-default-images.cozystack.io/name=%s (what the vm-default-images package renders with substrate proxmox)." $name (join ", " (keys $images | sortAlpha)) $name) -}}
{{- end -}}
{{- end -}}
{{- end -}}
