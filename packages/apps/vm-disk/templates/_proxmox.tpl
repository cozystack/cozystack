{{- /*
  substrate decides what a disk is: a CDI DataVolume on kubevirt, a Proxmox
  volume on a holder VM on proxmox. It is safe to choose only once: the two
  objects share nothing, so switching would render a new, empty disk under the
  same name while the old one still holds the data. The `## @immutable` marker
  is dashboard-only, which is why this reads the live object of the other
  substrate and refuses when it exists.

  Inert offline, like the other lookup guards in this chart: with no apiserver
  the lookups are empty and the render proceeds, so `helm template` and the
  unit tests are unaffected and a first install has nothing to compare against.
*/}}
{{- define "vm-disk.substrate" -}}
{{- $substrate := .Values.substrate | default "kubevirt" -}}
{{- if and (ne $substrate "kubevirt") (ne $substrate "proxmox") -}}
{{- fail (printf "substrate must be \"kubevirt\" or \"proxmox\", got %q" $substrate) -}}
{{- end -}}
{{- if eq $substrate "proxmox" -}}
{{- if lookup "cdi.kubevirt.io/v1beta1" "DataVolume" .Release.Namespace .Release.Name -}}
{{- fail (printf "vm-disk: %s already exists as a DataVolume (substrate kubevirt) and substrate is now \"proxmox\". A disk cannot move between hypervisors; create a new disk on the other substrate and copy the data." .Release.Name) -}}
{{- end -}}
{{- else -}}
{{- if lookup "proxmox.alperen.cloud/v1alpha1" "VirtualMachineVolume" .Release.Namespace .Release.Name -}}
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
