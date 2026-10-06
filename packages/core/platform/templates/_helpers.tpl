{{- define "cozystack.platform.package" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $root := default $ (index . 2) -}}
{{- $components := dict -}}
{{- if gt (len .) 3 -}}
{{- $components = index . 3 -}}
{{- end -}}
{{- $disabled := default (list) $root.Values.bundles.disabledPackages -}}
{{- if not (has $name $disabled) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
  annotations:
    helm.sh/resource-policy: keep
spec:
  variant: {{ $variant }}
{{- if $components }}
  components:
{{ toYaml $components | indent 4 }}
{{- end }}
{{- end }}
{{ end }}

{{- define "cozystack.platform.package.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- include "cozystack.platform.package" (list $name "default" $root) }}
{{ end }}

{{- define "cozystack.platform.package.optional" -}}
{{- $name := index . 0 -}}
{{- $variant := default "default" (index . 1) -}}
{{- $root := default $ (index . 2) -}}
{{- $disabled := default (list) $root.Values.bundles.disabledPackages -}}
{{- $enabled := default (list) $root.Values.bundles.enabledPackages -}}
{{- if and (has $name $enabled) (not (has $name $disabled)) -}}
---
apiVersion: cozystack.io/v1alpha1
kind: Package
metadata:
  name: {{ $name }}
  annotations:
    helm.sh/resource-policy: keep
spec:
  variant: {{ $variant }}
{{- end }}
{{ end }}

{{- define "cozystack.platform.package.optional.default" -}}
{{- $name := index . 0 -}}
{{- $root := index . 1 -}}
{{- include "cozystack.platform.package.optional" (list $name "default" $root) }}
{{ end }}

{{/*
Common system packages shared between isp-full and isp-full-generic bundles.
Does NOT include: networking (variant differs), linstor (talos.enabled differs)
*/}}
{{- define "cozystack.platform.system.common-packages" -}}
{{- $root := . -}}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-webhook" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.kubeovn-plunger" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.cozy-proxy" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.multus" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.metallb" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.reloader" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.linstor-scheduler" $root) }}
{{include "cozystack.platform.package.default" (list "cozystack.snapshot-controller" $root) }}
{{- /* securitygroup-controller maintains membership labels for CiliumNetworkPolicy-backed
       SecurityGroups, so it only makes sense where Cilium runs. Keeping it here with the
       other data-plane packages (rather than in the variant-agnostic body of system.yaml)
       keeps it out of the isp-hosted (networking=noop) variant, which never calls
       common-packages and has no cilium.io CRD for the controller to watch. */ -}}
{{include "cozystack.platform.package.default" (list "cozystack.securitygroup-controller" $root) }}
{{- end }}

{{/*
The block Cilium takes its own addresses from on the kube-ovn variants, the
last /21 of the pod CIDR, which kube-ovn is told to exclude. Called with the
pod CIDR, "cozystack.platform.ciliumPool" emits the block as a CIDR for the
Cilium operator and "cozystack.platform.ciliumPoolRange" as a first..last range
for kube-ovn excludeIps.

The block has to sit inside the pod CIDR rather than next to it. Envoy reaches
Gateway API backends from the node's Ingress IP, and kube-ovn masquerades
host-originated traffic only when its source lies in one of its subnets.
Outside them the backend's reply is routed to the backend's own node and lost.
The end of the range is the part kube-ovn reaches last, since it hands out
free addresses in ascending order and reuses released ones only once the rest
are gone. A /21 cut into /29s serves 256 nodes, the most a /16 cluster CIDR
sliced into /24s ever allowed.
*/}}
{{- define "cozystack.platform.ciliumPoolBounds" -}}
{{- $cidr := toString . -}}
{{- if not (regexMatch "^[0-9]{1,3}(\\.[0-9]{1,3}){3}/[0-9]{1,2}$" $cidr) -}}
{{- fail (printf "networking.podCIDR %q is not an IPv4 CIDR" $cidr) -}}
{{- end -}}
{{- $parts := splitList "/" $cidr -}}
{{- $prefix := atoi (last $parts) -}}
{{- if gt $prefix 20 -}}
{{- fail (printf "networking.podCIDR %s is too small. The kube-ovn variants reserve its last /21 for Cilium, so it needs a prefix of /20 or shorter" $cidr) -}}
{{- end -}}
{{- $address := 0 -}}
{{- range splitList "." (first $parts) -}}
{{- $address = add (mul $address 256) (atoi .) -}}
{{- end -}}
{{- $size := 1 -}}
{{- range until (sub 32 $prefix | int) -}}
{{- $size = mul $size 2 -}}
{{- end -}}
{{- $first := add (sub $address (mod $address $size)) (sub $size 2048) -}}
{{- printf "%s %s" (include "cozystack.platform.ipv4" $first) (include "cozystack.platform.ipv4" (add $first 2047)) -}}
{{- end }}

{{- define "cozystack.platform.ipv4" -}}
{{- printf "%d.%d.%d.%d" (div . 16777216) (mod (div . 65536) 256) (mod (div . 256) 256) (mod . 256) -}}
{{- end }}

{{- define "cozystack.platform.ciliumPool" -}}
{{- printf "%s/21" (first (splitList " " (include "cozystack.platform.ciliumPoolBounds" .))) -}}
{{- end }}

{{- define "cozystack.platform.ciliumPoolRange" -}}
{{- join ".." (splitList " " (include "cozystack.platform.ciliumPoolBounds" .)) -}}
{{- end }}
