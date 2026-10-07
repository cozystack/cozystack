{{- /*
IPv4 arithmetic for Proxmox-backed subnets. Helm has no address type, so an
address is carried as an int64 between these helpers. Only IPv4 is handled:
a Proxmox-backed subnet is refused for any other family.
*/}}
{{- define "vpc.ip4ToInt" -}}
{{- $o := splitList "." . -}}
{{- if ne (len $o) 4 -}}
{{- fail (printf "%q is not an IPv4 address" .) -}}
{{- end -}}
{{- range $o -}}
{{- if not (regexMatch "^(0|[1-9][0-9]{0,2})$" .) -}}
{{- fail (printf "%q is not an IPv4 address" $) -}}
{{- end -}}
{{- if gt (atoi .) 255 -}}
{{- fail (printf "%q is not an IPv4 address" $) -}}
{{- end -}}
{{- end -}}
{{- add (mul (atoi (index $o 0)) 16777216) (mul (atoi (index $o 1)) 65536) (mul (atoi (index $o 2)) 256) (atoi (index $o 3)) -}}
{{- end -}}

{{- define "vpc.intToIp4" -}}
{{- $n := int64 . -}}
{{- printf "%d.%d.%d.%d" (div $n 16777216) (mod (div $n 65536) 256) (mod (div $n 256) 256) (mod $n 256) -}}
{{- end -}}

{{- /* vpc.ip4Network: an IPv4 CIDR with its host bits cleared, as
      "a.b.c.d/len". A bare address is a /32. */ -}}
{{- define "vpc.ip4Network" -}}
{{- $p := splitList "/" . -}}
{{- $len := 32 -}}
{{- if eq (len $p) 2 -}}
{{- if not (regexMatch "^([0-9]|[12][0-9]|3[0-2])$" (index $p 1)) -}}
{{- fail (printf "%q is not an IPv4 CIDR" .) -}}
{{- end -}}
{{- $len = atoi (index $p 1) -}}
{{- else if ne (len $p) 1 -}}
{{- fail (printf "%q is not an IPv4 CIDR" .) -}}
{{- end -}}
{{- $base := include "vpc.ip4ToInt" (index $p 0) | atoi -}}
{{- $size := int64 1 -}}
{{- range until (sub 32 $len | int) -}}
{{- $size = mul $size 2 -}}
{{- end -}}
{{- printf "%s/%d" (include "vpc.intToIp4" (sub $base (mod $base $size))) $len -}}
{{- end -}}

{{- /*
vpc.proxmoxSubnets: facts for every subnet that carries `proxmox`, keyed by
subnet name, as YAML. Each entry:
  id, index, zone, cidr, prefix, gateway, vmFrom, vmTo
All render-time validation of the Proxmox fields lives here, so the template
that consumes the facts can trust them.
*/}}
{{- define "vpc.proxmoxSubnets" -}}
{{- $root := . -}}
{{- $vpcId := print "vpc-" (print .Release.Namespace "/" .Release.Name | sha256sum | trunc 6) -}}
{{- $zones := dict -}}
{{- $want := false -}}
{{- range .Values.subnets -}}
{{- if and (hasKey . "proxmox") (kindIs "map" .proxmox) -}}
{{- $want = true -}}
{{- end -}}
{{- end -}}
{{- /* Look the zones up only when a subnet asks for Proxmox, and only when the
      API is served: Helm's lookup fails the whole render on a group the
      cluster does not serve, which would break every VPC on a cluster
      without the opt-in proxmox-network package. */ -}}
{{- if $want -}}
{{- if not ($root.Capabilities.APIVersions.Has "proxmox.cozystack.io/v1alpha1") -}}
{{- fail "a subnet sets proxmox, but the proxmox.cozystack.io API is not installed; ask the platform administrator to enable the proxmox-network package" -}}
{{- end -}}
{{- $zoneList := lookup "proxmox.cozystack.io/v1alpha1" "ProxmoxNetworkZone" "" "" -}}
{{- range ($zoneList.items | default list) -}}
{{- $_ := set $zones .metadata.name . -}}
{{- end -}}
{{- end -}}
{{- $out := dict -}}
{{- range $i, $s := .Values.subnets -}}
{{- if and (hasKey $s "proxmox") (kindIs "map" $s.proxmox) -}}
{{- $name := $s.name -}}
{{- if not $s.cidr -}}
{{- fail (printf "subnet %q: a Proxmox-backed subnet needs a cidr" $name) -}}
{{- end -}}
{{- /* Kube-OVN applies allowSubnets only through the private-subnet ACL, and a
      Proxmox-backed subnet is not private, so the list would be accepted and
      silently do nothing. */ -}}
{{- if $s.allowSubnets -}}
{{- fail (printf "subnet %q: allowSubnets has no effect on a Proxmox-backed subnet, which is not private and accepts everything its VPC routes to it; remove allowSubnets from it" $name) -}}
{{- end -}}
{{- $parts := splitList "/" $s.cidr -}}
{{- if or (ne (len $parts) 2) (contains ":" $s.cidr) -}}
{{- fail (printf "subnet %q: cidr %q must be an IPv4 network; Proxmox-backed subnets are IPv4 only" $name $s.cidr) -}}
{{- end -}}
{{- $prefix := atoi (index $parts 1) -}}
{{- if or (lt $prefix 8) (gt $prefix 28) -}}
{{- fail (printf "subnet %q: prefix /%d is outside /8../28; a Proxmox-backed subnet needs room for a gateway, VMs and pods" $name $prefix) -}}
{{- end -}}
{{- $base := include "vpc.ip4ToInt" (index $parts 0) | atoi -}}
{{- $size := int64 1 -}}
{{- range until (sub 32 $prefix | int) -}}
{{- $size = mul $size 2 -}}
{{- end -}}
{{- if ne (mod $base $size) 0 -}}
{{- fail (printf "subnet %q: %q is not a network address (host bits set)" $name $s.cidr) -}}
{{- end -}}
{{- $gw := add $base 1 -}}
{{- $last := sub (add $base $size) 2 -}}{{- /* the address before broadcast */ -}}
{{- $from := add $base (div $size 4) -}}
{{- $to := sub (add $base (mul 3 (div $size 4))) 1 -}}
{{- with $s.proxmox.vmRange -}}
{{- $r := splitList "-" . -}}
{{- if ne (len $r) 2 -}}
{{- fail (printf "subnet %q: proxmox.vmRange %q must be first-last" $name .) -}}
{{- end -}}
{{- $from = include "vpc.ip4ToInt" (trim (index $r 0)) | atoi -}}
{{- $to = include "vpc.ip4ToInt" (trim (index $r 1)) | atoi -}}
{{- end -}}
{{- if gt $from $to -}}
{{- fail (printf "subnet %q: proxmox.vmRange ends before it starts" $name) -}}
{{- end -}}
{{- if or (le $from $gw) (gt $to $last) -}}
{{- fail (printf "subnet %q: proxmox.vmRange %s-%s must lie inside %s after the gateway %s and before the broadcast address" $name (include "vpc.intToIp4" $from) (include "vpc.intToIp4" $to) $s.cidr (include "vpc.intToIp4" $gw)) -}}
{{- end -}}
{{- $zone := $s.proxmox.zone | default "" -}}
{{- if not $zone -}}
{{- $names := keys $zones | sortAlpha -}}
{{- if eq (len $names) 1 -}}
{{- $zone = index $names 0 -}}
{{- else if eq (len $names) 0 -}}
{{- fail (printf "subnet %q: no ProxmoxNetworkZone exists; ask the platform administrator to enable the proxmox-network package" $name) -}}
{{- else -}}
{{- fail (printf "subnet %q: several ProxmoxNetworkZones exist (%s); set proxmox.zone" $name (join ", " $names)) -}}
{{- end -}}
{{- end -}}
{{- $id := print "subnet-" (print $root.Release.Namespace "/" $vpcId "/" $name | sha256sum | trunc 8) -}}
{{- $egress := "" -}}
{{- $egressNamespace := "" -}}
{{- $providerNetwork := "" -}}
{{- $mtu := 0 -}}
{{- with (get $zones $zone) -}}
{{- $providerNetwork = .spec.providerNetwork -}}
{{- $mtu = int (.spec.mtu | default 0) -}}
{{- if and .spec.egress .spec.egress.externalSubnet -}}
{{- $egress = .spec.egress.externalSubnet -}}
{{- $egressNamespace = .spec.egress.gatewayNamespace | default "" -}}
{{- end -}}
{{- end -}}
{{- $_ := set $out $name (dict
  "id" $id
  "index" $i
  "zone" $zone
  "egressSubnet" $egress
  "egressNamespace" $egressNamespace
  "providerNetwork" $providerNetwork
  "mtu" $mtu
  "cidr" $s.cidr
  "prefix" $prefix
  "base" $base
  "size" $size
  "gateway" (include "vpc.intToIp4" $gw)
  "vmFrom" (include "vpc.intToIp4" $from)
  "vmTo" (include "vpc.intToIp4" $to)) -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{- /*
vpc.assertRoutesStayInside: a static route may only point at a next hop inside
one of this VPC's own subnets, or at a peering link address when peers are
declared. A next hop anywhere else cannot be reached from a VPC router that has
no external connection, and refusing it keeps a tenant from aiming a route at
another tenant's gateway.
*/}}
{{- define "vpc.assertRoutesStayInside" -}}
{{- $ranges := list -}}
{{- range .Values.subnets -}}
{{- if and .cidr (not (contains ":" .cidr)) -}}
{{- $p := splitList "/" .cidr -}}
{{- $b := include "vpc.ip4ToInt" (index $p 0) | atoi -}}
{{- $n := int64 1 -}}
{{- range until (sub 32 (atoi (index $p 1)) | int) -}}
{{- $n = mul $n 2 -}}
{{- end -}}
{{- $ranges = append $ranges (list $b (sub (add $b $n) 1)) -}}
{{- end -}}
{{- end -}}
{{- if .Values.peers -}}
{{- $ranges = append $ranges (list (include "vpc.ip4ToInt" "169.254.0.0" | atoi) (include "vpc.ip4ToInt" "169.254.255.255" | atoi)) -}}
{{- end -}}
{{- range .Values.routes -}}
{{- if not (contains ":" .nextHopIP) -}}
{{- $hop := include "vpc.ip4ToInt" .nextHopIP | atoi -}}
{{- $inside := false -}}
{{- range $ranges -}}
{{- if and (ge $hop (index . 0)) (le $hop (index . 1)) -}}
{{- $inside = true -}}
{{- end -}}
{{- end -}}
{{- if not $inside -}}
{{- fail (printf "route to %s: next hop %s is outside every subnet of this VPC" .cidr .nextHopIP) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
vpc.assertProxmoxSubnetsReleasable: a Proxmox-backed subnet of this VPC that
disappears from the values (removed, or its proxmox setting dropped) while VMs
still hold addresses on it would turn back into an overlay switch under them.
Refuse that upgrade. Uninstalling the whole VPC renders nothing, so it is not
checked here; there the controller's finalizers keep the subnet, the VLAN and
the pool until the last address is released.
*/}}
{{- define "vpc.assertProxmoxSubnetsReleasable" -}}
{{- $root := index . 0 -}}
{{- $vpcId := index . 1 -}}
{{- $proxmox := index . 2 -}}
{{- $keep := dict -}}
{{- range $proxmox -}}
{{- $_ := set $keep .id true -}}
{{- end -}}
{{- $listed := dict -}}
{{- range $root.Values.subnets -}}
{{- $_ := set $listed .name true -}}
{{- end -}}
{{- if $root.Capabilities.APIVersions.Has "proxmox.cozystack.io/v1alpha1" -}}
{{- $existing := lookup "proxmox.cozystack.io/v1alpha1" "ProxmoxNetwork" $root.Release.Namespace "" -}}
{{- range ($existing.items | default list) -}}
{{- if and (eq (dig "metadata" "labels" "cozystack.io/vpcId" "" .) $vpcId) (not (hasKey $keep .metadata.name)) -}}
{{- $name := dig "metadata" "labels" "cozystack.io/subnetName" .metadata.name . -}}
{{- /* Kube-OVN keeps a subnet's localnet port, tagged with its VLAN, for as
      long as the logical switch exists, even once spec.vlan is gone. A
      subnet that stays under the same name therefore cannot drop proxmox:
      its VLAN could never be released safely. */ -}}
{{- if hasKey $listed $name -}}
{{- fail (printf "subnet %q is Proxmox-backed and its proxmox setting cannot be removed; remove the subnet and add it back under a new name" $name) -}}
{{- end -}}
{{- $used := dig "status" "ipPool" "used" 0 . | int -}}
{{- if gt $used 0 -}}
{{- fail (printf "subnet %q is Proxmox-backed and %d VM address(es) are still allocated on it; delete those machines before removing the subnet" $name $used) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- /*
vpc.egressPolicyRoutes: the Vpc policyRoutes that keep routed traffic away
from the egress gateway, as a YAML list, empty without egress. OVN applies
router policies after the routing table, and Kube-OVN reroutes the
Proxmox-backed sources to the gateway at 29100 (29150 when node-local) with
only the VPC's own subnets allowed above that, at 31000. Without these entries
the gateway would also take traffic a static route sends elsewhere, such as to
a peered VPC, and SNAT it onto the transit network.
  30500 allow  ip4.dst == <route>  every IPv4 route narrower than 0.0.0.0/0
                                   keeps its next hop; a default route leaves
                                   the gateway the Proxmox subnets' way out
Kube-OVN keeps one policy per priority and match, and uses 29000-29500, 30000,
30100 and 31000 itself, so 30500 is never shared with it.
Open: traffic from a Proxmox-backed subnet to another VPC's CIDR still goes
to the gateway, and only the transit router keeps it out of that VPC
(docs/proxmox-networking.md, sections 3.4 and 8, question 9 of section 12).
A drop of the platform's tenant address space belongs between the allows
and the reroute, at 30400, once a platform-owned object such as the
ProxmoxNetworkZone carries that address space. The chart values belong to
the tenant, so they cannot hold a boundary that protects others.
*/}}
{{- define "vpc.egressPolicyRoutes" -}}
{{- $out := list -}}
{{- if and .Values.egress .Values.egress.enabled -}}
{{- $seen := dict -}}
{{- range .Values.routes -}}
{{- if not (contains ":" .cidr) -}}
{{- $net := include "vpc.ip4Network" .cidr -}}
{{- if not (or (hasSuffix "/0" $net) (hasKey $seen $net)) -}}
{{- $_ := set $seen $net true -}}
{{- $out = append $out (dict "priority" 30500 "action" "allow" "match" (printf "ip4.dst == %s" $net)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{- /*
vpc.egressGateway: outbound access for the Proxmox-backed subnets of this VPC,
through a Kube-OVN VpcEgressGateway in the zone's gateway namespace. The VPC router reroutes traffic whose
source is one of those subnets and whose destination is outside the VPC to
the gateway pods, which SNAT it onto the zone's transit network. East-west
traffic inside the VPC keeps its higher-priority allow route, and static routes
keep theirs (vpc.egressPolicyRoutes). The gateway pods
sit on the Proxmox subnet and on the transit network, both carried by the
zone's trunk, so they run only on nodes where that provider network is ready.
*/}}
{{- define "vpc.egressGateway" -}}
{{- $root := index . 0 -}}
{{- $vpcId := index . 1 -}}
{{- $proxmox := index . 2 -}}
{{- if and $root.Values.egress $root.Values.egress.enabled -}}
{{- if not $proxmox -}}
{{- fail "egress.enabled needs at least one Proxmox-backed subnet: the gateway reroutes those subnets only" -}}
{{- end -}}
{{- $names := keys $proxmox | sortAlpha -}}
{{- $first := get $proxmox (index $names 0) -}}
{{- $ids := list -}}
{{- range $names -}}
{{- $px := get $proxmox . -}}
{{- if ne $px.zone $first.zone -}}
{{- fail (printf "egress.enabled: Proxmox-backed subnets span zones %q and %q; one egress gateway serves one zone" $first.zone $px.zone) -}}
{{- end -}}
{{- $ids = append $ids $px.id -}}
{{- end -}}
{{- if not $first.egressSubnet -}}
{{- fail (printf "egress.enabled: zone %q declares no egress.externalSubnet" $first.zone) -}}
{{- end -}}
{{- if not $first.egressNamespace -}}
{{- fail (printf "egress.enabled: zone %q declares no egress.gatewayNamespace" $first.zone) -}}
{{- end -}}
{{- $internal := $first.id -}}
{{- with $root.Values.egress.internalCidr }}
{{- /* A dedicated overlay subnet for the gateway pods' primary interface,
      for when the first Proxmox subnet should not host it. */ -}}
{{- $internal = print "subnet-" (print $root.Release.Namespace "/" $vpcId "/__egress" | sha256sum | trunc 8) }}
---
apiVersion: kubeovn.io/v1
kind: Subnet
metadata:
  name: {{ $internal }}
  labels:
    cozystack.io/egressSubnet: "true"
    cozystack.io/vpcId: {{ $vpcId }}
    cozystack.io/vpcName: {{ $root.Release.Name }}
    cozystack.io/tenantName: {{ $root.Release.Namespace }}
spec:
  vpc: {{ $vpcId }}
  cidrBlock: {{ . | quote }}
  protocol: IPv4
  enableLb: false
  private: false
{{- end }}
---
{{- /* The gateway runs in the zone's platform namespace: its pods need
      NET_ADMIN and a privileged init container, which the tenant namespace's
      Pod Security level refuses. Its name carries the VPC's hashed id, so
      gateways of different tenants never collide there. */}}
apiVersion: kubeovn.io/v1
kind: VpcEgressGateway
metadata:
  name: {{ $vpcId }}-egress
  namespace: {{ $first.egressNamespace }}
  labels:
    cozystack.io/vpcId: {{ $vpcId }}
    cozystack.io/vpcName: {{ $root.Release.Name }}
    cozystack.io/tenantName: {{ $root.Release.Namespace }}
spec:
  vpc: {{ $vpcId }}
  replicas: {{ $root.Values.egress.replicas | default 1 }}
  internalSubnet: {{ $internal }}
  externalSubnet: {{ $first.egressSubnet }}
  {{- with $first.providerNetwork }}
  nodeSelector:
    - matchLabels:
        {{ printf "%s.provider-network.kubernetes.io/ready" . }}: "true"
  {{- end }}
  policies:
    - snat: true
      subnets:
        {{- toYaml $ids | nindent 8 }}
{{- end -}}
{{- end -}}
