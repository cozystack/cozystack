{{- /*
cozy-lib.proxmox.subnetId: the Kube-OVN Subnet name the vpc chart gives subnet
`subnet` of VPC application `vpc` in `namespace`. The vpc chart names a VPC
after its release ("virtualprivatecloud-<vpc>") and a subnet after the VPC, so
any chart can compute the name without a lookup. Keep in sync with
packages/apps/vpc/templates/vpc.yaml.

Usage: include "cozy-lib.proxmox.subnetId" (dict "namespace" ns "vpc" vpc "subnet" subnet)
*/}}
{{- define "cozy-lib.proxmox.subnetId" -}}
{{- $vpcId := print "vpc-" (print .namespace "/virtualprivatecloud-" .vpc | sha256sum | trunc 6) -}}
{{- print "subnet-" (print .namespace "/" $vpcId "/" .subnet | sha256sum | trunc 8) -}}
{{- end -}}

{{- /*
cozy-lib.proxmox.network: resolves a reference to a Proxmox-backed VPC subnet
into what a capmox NIC needs, read from the ProxmoxNetwork the vpc chart and
the proxmox-network controller maintain in the same namespace. Only the
release namespace is ever read, so a reference cannot reach another tenant's
network.

Returns YAML: {id, vlan, bridge, mtu, gateway, prefix, pool}. Fails the render
with the network's own condition while it is not Ready, so the HelmRelease
shows why the machines are not created.

Usage: include "cozy-lib.proxmox.network" (dict "namespace" ns "vpc" vpc "subnet" subnet "field" "proxmox.network")
*/}}
{{- define "cozy-lib.proxmox.network" -}}
{{- if not (and .vpc .subnet) -}}
{{- fail (printf "%s: set both vpc and subnet" .field) -}}
{{- end -}}
{{- $id := include "cozy-lib.proxmox.subnetId" . -}}
{{- $net := lookup "proxmox.cozystack.io/v1alpha1" "ProxmoxNetwork" .namespace $id -}}
{{- if not $net -}}
{{- fail (printf "%s: subnet %q of VPC %q is not Proxmox-backed (no ProxmoxNetwork %s in %s); add proxmox: {} to the subnet in the VPC application" .field .subnet .vpc $id .namespace) -}}
{{- end -}}
{{- $ready := dict -}}
{{- range (dig "status" "conditions" list $net) -}}
{{- if eq .type "Ready" -}}
{{- $ready = . -}}
{{- end -}}
{{- end -}}
{{- if ne (get $ready "status") "True" -}}
{{- fail (printf "%s: Proxmox network %s/%s (VPC %q subnet %q) is not Ready: %s: %s" .field .namespace $id .vpc .subnet (get $ready "reason" | default "Pending") (get $ready "message" | default "the controller has not reported yet")) -}}
{{- end -}}
{{- $st := $net.status -}}
{{- dict
  "id" $id
  "vlan" (int64 $st.vlan)
  "bridge" $st.bridge
  "mtu" (dig "mtu" 0 $st | int64)
  "gateway" $st.gateway
  "prefix" (int64 $st.prefix)
  "pool" $net.spec.ipPool
  | toYaml -}}
{{- end -}}
