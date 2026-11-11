{{- /*
cozy-lib.proxmox.network: resolves the name of a Proxmox tenant network into
what a capmox NIC and a ProxmoxCluster need, read from the ProxmoxNetworkZone
table the proxmox-network package renders and from the network's
InClusterIPPool in the release namespace. The zones are cluster-scoped and
list every tenant's networks; only the entry whose namespace is the release
namespace is taken, so a name cannot reach another tenant's network.

Returns YAML: {zone, bridge, vlan, mtu, gateway, prefix, pool}. mtu is the
network's, else the zone's, else 0 (unset). Fails the render with a message
naming what is missing, so the tenant's HelmRelease shows why no machine is
created: the network is not in any zone (ask the platform operator), it is
listed twice (an operator error the package refuses to render, so only a
hand-edited zone), or its pool is not rendered yet.

Usage: include "cozy-lib.proxmox.network" (dict "namespace" ns "name" name "field" "proxmox.network")
*/}}
{{- define "cozy-lib.proxmox.network" -}}
{{- if not .name -}}
{{- fail (printf "%s.name: set the name of a Proxmox tenant network of this namespace" .field) -}}
{{- end -}}
{{- $found := list -}}
{{- range $zone := (lookup "proxmox.cozystack.io/v1alpha1" "ProxmoxNetworkZone" "" "" | dig "items" list) -}}
{{- range $net := (dig "spec" "networks" list $zone) -}}
{{- if and (eq (toString $net.namespace) $.namespace) (eq (toString $net.name) $.name) -}}
{{- $found = append $found (dict "zone" $zone "net" $net) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if not $found -}}
{{- fail (printf "%s: no Proxmox tenant network %q for namespace %s: ask the platform operator to add it to a ProxmoxNetworkZone (proxmox-network package)" .field .name .namespace) -}}
{{- end -}}
{{- if gt (len $found) 1 -}}
{{- $names := list -}}
{{- range $found -}}{{- $names = append $names .zone.metadata.name -}}{{- end -}}
{{- fail (printf "%s: Proxmox tenant network %s/%s is listed in more than one zone (%s); the platform operator has to keep one" .field .namespace .name (join ", " $names)) -}}
{{- end -}}
{{- $zone := (first $found).zone -}}
{{- $net := (first $found).net -}}
{{- $poolName := printf "pxnet-%s" .name -}}
{{- if not (lookup "ipam.cluster.x-k8s.io/v1alpha2" "InClusterIPPool" .namespace $poolName) -}}
{{- fail (printf "%s: the IP pool %s/%s of Proxmox tenant network %q is not rendered yet; the proxmox-network package creates it from the zone table" .field .namespace $poolName .name) -}}
{{- end -}}
{{- $mtu := 0 -}}
{{- if hasKey $zone.spec "mtu" -}}{{- $mtu = int64 $zone.spec.mtu -}}{{- end -}}
{{- if hasKey $net "mtu" -}}{{- $mtu = int64 $net.mtu -}}{{- end -}}
{{- dict
  "zone" $zone.metadata.name
  "bridge" $zone.spec.bridge
  "vlan" (int64 $net.vlan)
  "mtu" $mtu
  "gateway" (toString $net.gateway)
  "prefix" (int64 $net.prefix)
  "pool" $poolName
  | toYaml -}}
{{- end -}}
