{{- /* The substrate, validated: only the two hypervisors the charts know. */}}
{{- define "vm-default-images.substrate" -}}
{{- $substrate := .Values.substrate | default "kubevirt" -}}
{{- if and (ne $substrate "kubevirt") (ne $substrate "proxmox") -}}
{{- fail (printf "substrate must be \"kubevirt\" or \"proxmox\", got %q" $substrate) -}}
{{- end -}}
{{- $substrate -}}
{{- end -}}

{{- /*
  The platform's Proxmox catalog, as JSON: the ConfigMap the proxmox-vm
  package renders into cozy-public. The same helper as in vm-disk; a define
  cannot be shared across charts without a library, and the catalog's shape
  is the contract between the package and every chart that reads it.
*/}}
{{- define "vm-default-images.proxmoxCatalog" -}}
{{- $cm := lookup "v1" "ConfigMap" "cozy-public" "proxmox-vm-catalog" -}}
{{- if not $cm -}}
{{- fail "substrate proxmox needs the platform's proxmox-vm package: the catalog ConfigMap cozy-public/proxmox-vm-catalog does not exist. Enable cozystack.proxmox-vm in bundles.enabledPackages and fill its catalog." -}}
{{- end -}}
{{- $catalog := dict -}}
{{- range $key, $value := $cm.data -}}
{{- if or (eq $key "connection") (eq $key "defaultNode") -}}
{{- $_ := set $catalog $key $value -}}
{{- else if or (eq $key "allowedNodes") (eq $key "dnsServers") -}}
{{- $_ := set $catalog $key ($value | fromYamlArray) -}}
{{- else -}}
{{- $_ := set $catalog $key ($value | fromYaml) -}}
{{- end -}}
{{- end -}}
{{- $catalog | toJson -}}
{{- end -}}

{{- /* Whole GiB a quantity needs: 20Gi -> 20, 1500Mi -> 2. */}}
{{- define "vm-default-images.gib" -}}
{{- $bytes := include "cozy-lib.resources.toFloat" . | float64 -}}
{{- if le $bytes 0.0 -}}
{{- fail (printf "storage (%s) must be a positive quantity" .) -}}
{{- end -}}
{{- divf $bytes 1073741824.0 | ceil | int -}}
{{- end -}}
